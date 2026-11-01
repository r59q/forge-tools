package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/broker/api"
	"github.com/r59q/forge-tools/forge-connector/broker/sqlite"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	annotations "google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

const adminToken = "test-admin-credential"
const forgeToken = "test-forge-credential-one"
const rotatedToken = "test-forge-credential-two"

type wire struct {
	invoke func(string, proto.Message, proto.Message, []string) error
	raw    func(string, string, http.Header) (int, string)
	close  func()
}

func transport(t *testing.T, kind string, s *api.Server) wire {
	t.Helper()
	if kind == "grpc" {
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		pb.RegisterBrokerServer(server, s)
		go func() { _ = server.Serve(listener) }()
		conn, err := grpc.NewClient("passthrough:///broker", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
		if err != nil {
			t.Fatal(err)
		}
		close := func() { conn.Close(); server.Stop(); listener.Close() }
		return wire{close: close, invoke: func(method string, in, out proto.Message, auth []string) error {
			md := metadata.MD{"authorization": auth}
			ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), md), 5*time.Second)
			defer cancel()
			return conn.Invoke(ctx, "/forge.connector.v1.Broker/"+method, in, out)
		}}
	}
	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	raw := func(method, path string, headers http.Header, body []byte) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Error("response permits secret caching")
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	return wire{close: server.Close, raw: func(path, body string, h http.Header) (int, string) {
		code, data := raw("POST", path, h, []byte(body))
		return code, string(data)
	}, invoke: func(method string, in, out proto.Message, auth []string) error {
		descriptor := pb.File_broker_proto.Services().ByName("Broker").Methods().ByName(protoreflect.Name(method))
		rule := proto.GetExtension(descriptor.Options(), annotations.E_Http).(*annotations.HttpRule)
		var verb, path string
		switch p := rule.Pattern.(type) {
		case *annotations.HttpRule_Get:
			verb, path = "GET", p.Get
		case *annotations.HttpRule_Post:
			verb, path = "POST", p.Post
		case *annotations.HttpRule_Delete:
			verb, path = "DELETE", p.Delete
		default:
			t.Fatal("unsupported HTTP binding")
		}
		for _, field := range []string{"id", "connection_id"} {
			if strings.Contains(path, "{"+field+"}") {
				value := in.ProtoReflect().Get(in.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(field))).String()
				path = strings.ReplaceAll(path, "{"+field+"}", value)
			}
		}
		body, err := protojson.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		code, data := raw(verb, path, http.Header{"Authorization": auth}, body)
		if code >= 400 {
			var problem struct {
				Code    int
				Message string
			}
			if err := json.Unmarshal(data, &problem); err != nil {
				t.Fatalf("error response: %s", data)
			}
			return status.Error(codes.Code(problem.Code), problem.Message)
		}
		return protojson.Unmarshal(data, out)
	}}
}

func openServer(t *testing.T, path, adminFile string, key []byte) (*sqlite.Store, *api.Server) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := broker.NewAESGCMSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	var reg forgeauth.Registry
	if err := reg.Register(github.Provider{}); err != nil {
		t.Fatal(err)
	}
	manager, err := broker.NewManager(store, sealer, &reg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.New(manager, forgeauth.FileToken(adminFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return store, server
}

func TestTransports(t *testing.T) {
	for _, kind := range []string{"grpc", "rest"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "broker.db")
			adminFile := filepath.Join(dir, "admin")
			if err := os.WriteFile(adminFile, []byte(adminToken), 0600); err != nil {
				t.Fatal(err)
			}
			key := bytes.Repeat([]byte{7}, 32)
			var revoked atomic.Bool
			forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if revoked.Load() || r.URL.Path != "/user" || (token != forgeToken && token != rotatedToken) {
					w.WriteHeader(401)
					fmt.Fprint(w, "rejected "+token)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "owner", "name": "echo " + token})
			}))
			defer forge.Close()
			store, server := openServer(t, dbPath, adminFile, key)
			w := transport(t, kind, server)
			defer func() { w.close(); store.Close() }()
			invoke := func(method string, in, out proto.Message, token string, want codes.Code) {
				t.Helper()
				var auth []string
				if token != "" {
					auth = []string{"Bearer " + token}
				}
				err := w.invoke(method, in, out, auth)
				if status.Code(err) != want {
					t.Fatalf("%s: %v, want %v", method, err, want)
				}
				if err != nil && (strings.Contains(err.Error(), forgeToken) || strings.Contains(err.Error(), rotatedToken) || strings.Contains(err.Error(), adminToken)) {
					t.Fatal("error leaked a credential")
				}
			}
			// Every admin RPC must authenticate before using its input or accessing data.
			adminRPCs := []struct {
				name          string
				input, output proto.Message
			}{
				{"CreateForge", &pb.CreateForgeRequest{}, &pb.Forge{}},
				{"ListForges", &emptypb.Empty{}, &pb.ListForgesResponse{}},
				{"DeleteForge", &pb.ForgeRequest{Id: "missing"}, &emptypb.Empty{}},
				{"CreateConnection", &pb.CreateConnectionRequest{}, &pb.Connection{}},
				{"ListConnections", &emptypb.Empty{}, &pb.ListConnectionsResponse{}},
				{"GetConnection", &pb.ConnectionRequest{Id: "missing"}, &pb.Connection{}},
				{"TestConnection", &pb.ConnectionRequest{Id: "missing"}, &pb.Connection{}},
				{"RotateCredential", &pb.RotateCredentialRequest{Id: "missing", Credential: forgeToken}, &pb.Connection{}},
				{"DeleteConnection", &pb.ConnectionRequest{Id: "missing"}, &emptypb.Empty{}},
				{"CreateClient", &pb.CreateClientRequest{}, &pb.CreateClientResponse{}},
				{"ListClients", &emptypb.Empty{}, &pb.ListClientsResponse{}},
				{"DeleteClient", &pb.ClientRequest{Id: "missing"}, &emptypb.Empty{}},
			}
			for _, rpc := range adminRPCs {
				for _, token := range []string{"", "wrong"} {
					invoke(rpc.name, rpc.input, rpc.output, token, codes.Unauthenticated)
				}
			}
			f := &pb.Forge{}
			invoke("CreateForge", &pb.CreateForgeRequest{Kind: "github", BaseUrl: forge.URL, Name: "local"}, f, adminToken, codes.OK)
			conn := &pb.Connection{}
			invoke("CreateConnection", &pb.CreateConnectionRequest{ForgeId: f.Id, Credential: forgeToken}, conn, adminToken, codes.OK)
			if conn.Id == "" || conn.Status != "ok" || conn.Identity.Login != "owner" || strings.Contains(conn.Identity.Name, forgeToken) {
				t.Fatal("invalid or secret-bearing connection metadata")
			}
			issued := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{ConnectionIds: []string{conn.Id}, CanIssueTokens: true}, issued, adminToken, codes.OK)
			if issued.Token == "" || len(issued.Client.ConnectionIds) != 1 {
				t.Fatal("missing client bootstrap")
			}
			for _, rpc := range adminRPCs {
				invoke(rpc.name, rpc.input, rpc.output, issued.Token, codes.Unauthenticated)
			}
			for _, token := range []string{"", "wrong", adminToken} {
				invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, token, codes.Unauthenticated)
				invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.Client{}, token, codes.Unauthenticated)
				invoke("ListClientConnections", &emptypb.Empty{}, &pb.ListClientConnectionsResponse{}, token, codes.Unauthenticated)
			}
			for _, auth := range [][]string{{"Basic " + adminToken}, {"Bearer "}, {"Bearer " + adminToken, "Bearer " + adminToken}, {"Bearer bad token"}} {
				if err := w.invoke("ListForges", &emptypb.Empty{}, &pb.ListForgesResponse{}, auth); status.Code(err) != codes.Unauthenticated {
					t.Fatalf("malformed admin authorization accepted: %v", err)
				}
			}
			for _, auth := range [][]string{{"Basic " + issued.Token}, {"Bearer " + issued.Token, "Bearer " + issued.Token}} {
				if err := w.invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, auth); status.Code(err) != codes.Unauthenticated {
					t.Fatalf("malformed client authorization accepted: %v", err)
				}
			}
			if kind == "rest" {
				code, _ := w.raw("/v1/connections/"+conn.Id+"/token", "{}", http.Header{"Grpc-Metadata-Authorization": []string{"Bearer " + issued.Token}})
				if code != 401 {
					t.Fatalf("metadata header spoof: %d", code)
				}
				code, body := w.raw("/v1/connections/"+conn.Id+"/credential", `{"credential": "`+forgeToken+`", "unknown-`+forgeToken+`": "`+forgeToken+`"}`, http.Header{"Authorization": []string{"Bearer " + adminToken}})
				if code != 400 || strings.Contains(body, forgeToken) {
					t.Fatal("parse error leaked input or accepted unknown field")
				}
			}
			otherConnection := &pb.Connection{}
			invoke("CreateConnection", &pb.CreateConnectionRequest{ForgeId: f.Id, Credential: forgeToken}, otherConnection, adminToken, codes.OK)
			for _, method := range []string{"IssueToken", "VerifyClient"} {
				var input, output proto.Message = &pb.IssueTokenRequest{ConnectionId: otherConnection.Id}, &pb.IssueTokenResponse{}
				if method == "VerifyClient" {
					input, output = &pb.VerifyClientRequest{ConnectionId: otherConnection.Id}, &pb.Client{}
				}
				invoke(method, input, output, issued.Token, codes.PermissionDenied)
			}
			invoke("DeleteConnection", &pb.ConnectionRequest{Id: otherConnection.Id}, &emptypb.Empty{}, adminToken, codes.OK)
			verifyOnly := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{ConnectionIds: []string{conn.Id}}, verifyOnly, adminToken, codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.Client{}, verifyOnly.Token, codes.OK)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, verifyOnly.Token, codes.PermissionDenied)
			listed := &pb.ListClientConnectionsResponse{}
			invoke("ListClientConnections", &emptypb.Empty{}, listed, verifyOnly.Token, codes.OK)
			if len(listed.Connections) != 1 || listed.Connections[0].Id != conn.Id || listed.Connections[0].Kind != "github" || listed.Connections[0].Status != "ok" {
				t.Fatalf("ListClientConnections = %v", listed)
			}
			emptyScope := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{CanIssueTokens: true}, emptyScope, adminToken, codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.Client{}, emptyScope.Token, codes.PermissionDenied)
			tokenReply := &pb.IssueTokenResponse{}
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, tokenReply, issued.Token, codes.OK)
			if tokenReply.Token != forgeToken || tokenReply.Kind != "github" || tokenReply.BaseUrl != forge.URL {
				t.Fatal("issued wrong credential")
			}
			invoke("RotateCredential", &pb.RotateCredentialRequest{Id: conn.Id, Credential: "rejected-" + forgeToken}, &pb.Connection{}, adminToken, codes.InvalidArgument)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, tokenReply, issued.Token, codes.OK)
			if tokenReply.Token != forgeToken {
				t.Fatal("failed rotation changed credential")
			}
			invoke("RotateCredential", &pb.RotateCredentialRequest{Id: conn.Id, Credential: rotatedToken}, &pb.Connection{}, adminToken, codes.OK)
			invoke("TestConnection", &pb.ConnectionRequest{Id: conn.Id}, &pb.Connection{}, adminToken, codes.OK)
			revoked.Store(true)
			invoke("TestConnection", &pb.ConnectionRequest{Id: conn.Id}, &pb.Connection{}, adminToken, codes.InvalidArgument)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.Client{}, issued.Token, codes.FailedPrecondition)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, issued.Token, codes.FailedPrecondition)
			revoked.Store(false)
			invoke("TestConnection", &pb.ConnectionRequest{Id: conn.Id}, &pb.Connection{}, adminToken, codes.OK)
			for _, rpc := range []struct {
				name   string
				output proto.Message
			}{{"ListForges", &pb.ListForgesResponse{}}, {"ListConnections", &pb.ListConnectionsResponse{}}, {"ListClients", &pb.ListClientsResponse{}}} {
				invoke(rpc.name, &emptypb.Empty{}, rpc.output, adminToken, codes.OK)
				data, err := protojson.Marshal(rpc.output)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{adminToken, forgeToken, rotatedToken, issued.Token} {
					if bytes.Contains(data, []byte(secret)) {
						t.Fatal("metadata leaked secret")
					}
				}
			}
			// Stop both transports and the database, then recreate the service with its key.
			w.close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(dbPath + "*")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{adminToken, forgeToken, rotatedToken, issued.Token} {
					if bytes.Contains(data, []byte(secret)) {
						t.Fatal("database contains plaintext secret")
					}
				}
			}
			store, server = openServer(t, dbPath, adminFile, key)
			w = transport(t, kind, server)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, tokenReply, issued.Token, codes.OK)
			if tokenReply.Token != rotatedToken {
				t.Fatal("restart lost rotated credential")
			}
			invoke("GetConnection", &pb.ConnectionRequest{Id: conn.Id}, &pb.Connection{}, adminToken, codes.OK)
			// Admin credentials are reread, never persisted.
			if err := os.WriteFile(adminFile, []byte("changed-admin"), 0600); err != nil {
				t.Fatal(err)
			}
			invoke("ListClients", &emptypb.Empty{}, &pb.ListClientsResponse{}, adminToken, codes.Unauthenticated)
			invoke("DeleteClient", &pb.ClientRequest{Id: issued.Client.Id}, &emptypb.Empty{}, "changed-admin", codes.OK)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, issued.Token, codes.Unauthenticated)
			invoke("DeleteForge", &pb.ForgeRequest{Id: f.Id}, &emptypb.Empty{}, "changed-admin", codes.FailedPrecondition)
			invoke("DeleteConnection", &pb.ConnectionRequest{Id: conn.Id}, &emptypb.Empty{}, "changed-admin", codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.Client{}, verifyOnly.Token, codes.PermissionDenied)
			invoke("DeleteForge", &pb.ForgeRequest{Id: f.Id}, &emptypb.Empty{}, "changed-admin", codes.OK)
		})
	}
}

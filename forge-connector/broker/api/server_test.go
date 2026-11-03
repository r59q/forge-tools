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
		pb.RegisterBrokerServiceServer(server, s)
		go func() { _ = server.Serve(listener) }()
		conn, err := grpc.NewClient(
			"passthrough:///broker",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		)
		if err != nil {
			t.Fatal(err)
		}

		close := func() {
			conn.Close()
			server.Stop()
			listener.Close()
		}

		return wire{close: close, invoke: func(method string, in, out proto.Message, auth []string) error {
			md := metadata.MD{"authorization": auth}
			ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), md), 5*time.Second)
			defer cancel()

			return conn.Invoke(ctx, "/forge.connector.v1.BrokerService/"+method, in, out)
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

	return wire{
		close: server.Close,
		raw: func(path, body string, h http.Header) (int, string) {
			code, data := raw("POST", path, h, []byte(body))
			return code, string(data)
		},
		invoke: func(method string, in, out proto.Message, auth []string) error {
			descriptor := pb.File_forge_connector_v1_broker_proto.Services().ByName("BrokerService").Methods().ByName(protoreflect.Name(method))
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

			// REST returns only the response_body field, as the gateway does.
			if rule.ResponseBody != "" {
				field := out.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(rule.ResponseBody))
				out = out.ProtoReflect().Mutable(field).Message().Interface()
			}

			return protojson.Unmarshal(data, out)
		},
	}
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
			defer func() {
				w.close()
				store.Close()
			}()
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
				if err != nil && (strings.Contains(err.Error(), forgeToken) ||
					strings.Contains(err.Error(), rotatedToken) || strings.Contains(err.Error(), adminToken)) {
					t.Fatal("error leaked a credential")
				}
			}
			// Every admin RPC must authenticate before using its input or accessing data.
			adminRPCs := []struct {
				name          string
				input, output proto.Message
			}{
				{"CreateForge", &pb.CreateForgeRequest{}, &pb.CreateForgeResponse{}},
				{"ListForges", &pb.ListForgesRequest{}, &pb.ListForgesResponse{}},
				{"DeleteForge", &pb.DeleteForgeRequest{Id: "missing"}, &pb.DeleteForgeResponse{}},
				{"CreateConnection", &pb.CreateConnectionRequest{}, &pb.CreateConnectionResponse{}},
				{"ListConnections", &pb.ListConnectionsRequest{}, &pb.ListConnectionsResponse{}},
				{"GetConnection", &pb.GetConnectionRequest{Id: "missing"}, &pb.GetConnectionResponse{}},
				{"TestConnection", &pb.TestConnectionRequest{Id: "missing"}, &pb.TestConnectionResponse{}},
				{"RotateCredential", &pb.RotateCredentialRequest{Id: "missing", Credential: forgeToken}, &pb.RotateCredentialResponse{}},
				{"DeleteConnection", &pb.DeleteConnectionRequest{Id: "missing"}, &pb.DeleteConnectionResponse{}},
				{"CreateClient", &pb.CreateClientRequest{}, &pb.CreateClientResponse{}},
				{"ListClients", &pb.ListClientsRequest{}, &pb.ListClientsResponse{}},
				{"DeleteClient", &pb.DeleteClientRequest{Id: "missing"}, &pb.DeleteClientResponse{}},
			}
			for _, rpc := range adminRPCs {
				for _, token := range []string{"", "wrong"} {
					invoke(rpc.name, rpc.input, rpc.output, token, codes.Unauthenticated)
				}
			}

			created := &pb.CreateForgeResponse{}
			invoke("CreateForge", &pb.CreateForgeRequest{
				Kind:       "github",
				BaseUrl:    forge.URL,
				GitBaseUrl: "ftp://git.example",
			}, created, adminToken, codes.InvalidArgument)
			invoke("CreateForge", &pb.CreateForgeRequest{
				Kind:       "github",
				BaseUrl:    forge.URL,
				Name:       "local",
				GitBaseUrl: forge.URL + "/git/",
			}, created, adminToken, codes.OK)
			f := created.GetForge()
			if f.GitBaseUrl != forge.URL+"/git" {
				t.Fatalf("git base URL = %q", f.GitBaseUrl)
			}

			connCreated := &pb.CreateConnectionResponse{}
			invoke("CreateConnection", &pb.CreateConnectionRequest{ForgeId: f.Id, Credential: forgeToken}, connCreated, adminToken, codes.OK)
			conn := connCreated.GetConnection()
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
				invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.VerifyClientResponse{}, token, codes.Unauthenticated)
				invoke("ListClientConnections", &pb.ListClientConnectionsRequest{}, &pb.ListClientConnectionsResponse{}, token, codes.Unauthenticated)
			}

			for _, auth := range [][]string{{"Basic " + adminToken}, {"Bearer "}, {"Bearer " + adminToken, "Bearer " + adminToken}, {"Bearer bad token"}} {
				if err := w.invoke("ListForges", &pb.ListForgesRequest{}, &pb.ListForgesResponse{}, auth); status.Code(err) != codes.Unauthenticated {
					t.Fatalf("malformed admin authorization accepted: %v", err)
				}
			}

			for _, auth := range [][]string{{"Basic " + issued.Token}, {"Bearer " + issued.Token, "Bearer " + issued.Token}} {
				if err := w.invoke(
					"IssueToken",
					&pb.IssueTokenRequest{ConnectionId: conn.Id},
					&pb.IssueTokenResponse{},
					auth,
				); status.Code(err) != codes.Unauthenticated {
					t.Fatalf("malformed client authorization accepted: %v", err)
				}
			}

			if kind == "rest" {
				code, _ := w.raw(
					"/v1/connections/"+conn.Id+"/token",
					"{}",
					http.Header{"Grpc-Metadata-Authorization": []string{"Bearer " + issued.Token}},
				)
				if code != 401 {
					t.Fatalf("metadata header spoof: %d", code)
				}

				code, body := w.raw(
					"/v1/connections/"+conn.Id+"/credential",
					`{"credential": "`+forgeToken+`", "unknown-`+forgeToken+`": "`+forgeToken+`"}`,
					http.Header{"Authorization": []string{"Bearer " + adminToken}},
				)
				if code != 400 || strings.Contains(body, forgeToken) {
					t.Fatal("parse error leaked input or accepted unknown field")
				}
			}

			otherConnectionCreated := &pb.CreateConnectionResponse{}
			invoke("CreateConnection", &pb.CreateConnectionRequest{ForgeId: f.Id, Credential: forgeToken}, otherConnectionCreated, adminToken, codes.OK)
			otherConnection := otherConnectionCreated.GetConnection()
			for _, method := range []string{"IssueToken", "VerifyClient"} {
				var input, output proto.Message = &pb.IssueTokenRequest{ConnectionId: otherConnection.Id}, &pb.IssueTokenResponse{}
				if method == "VerifyClient" {
					input, output = &pb.VerifyClientRequest{ConnectionId: otherConnection.Id}, &pb.VerifyClientResponse{}
				}

				invoke(method, input, output, issued.Token, codes.PermissionDenied)
			}

			invoke("DeleteConnection", &pb.DeleteConnectionRequest{Id: otherConnection.Id}, &pb.DeleteConnectionResponse{}, adminToken, codes.OK)
			verifyOnly := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{ConnectionIds: []string{conn.Id}}, verifyOnly, adminToken, codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.VerifyClientResponse{}, verifyOnly.Token, codes.OK)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, verifyOnly.Token, codes.PermissionDenied)
			listed := &pb.ListClientConnectionsResponse{}
			invoke("ListClientConnections", &pb.ListClientConnectionsRequest{}, listed, verifyOnly.Token, codes.OK)
			if len(listed.Connections) != 1 ||
				listed.Connections[0].Id != conn.Id || listed.Connections[0].Kind != "github" || listed.Connections[0].Status != "ok" {
				t.Fatalf("ListClientConnections = %v", listed)
			}

			invoke(
				"CreateClient",
				&pb.CreateClientRequest{AllConnections: true, ConnectionIds: []string{conn.Id}},
				&pb.CreateClientResponse{},
				adminToken,
				codes.InvalidArgument,
			)
			everything := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{AllConnections: true}, everything, adminToken, codes.OK)
			if !everything.Client.AllConnections || len(everything.Client.ConnectionIds) != 0 {
				t.Fatalf("all-connections client = %v", everything.Client)
			}

			chosen := strings.Repeat("c", 40)
			registered := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{AllConnections: true, Token: chosen}, registered, adminToken, codes.OK)
			invoke("CreateClient", &pb.CreateClientRequest{Token: chosen}, &pb.CreateClientResponse{}, adminToken, codes.FailedPrecondition)
			invoke("CreateClient", &pb.CreateClientRequest{Token: "short"}, &pb.CreateClientResponse{}, adminToken, codes.InvalidArgument)
			invoke("ListClientConnections", &pb.ListClientConnectionsRequest{}, &pb.ListClientConnectionsResponse{}, chosen, codes.OK)
			invoke("DeleteClient", &pb.DeleteClientRequest{Id: registered.Client.Id}, &pb.DeleteClientResponse{}, adminToken, codes.OK)
			laterCreated := &pb.CreateConnectionResponse{}
			invoke("CreateConnection", &pb.CreateConnectionRequest{ForgeId: f.Id, Credential: forgeToken}, laterCreated, adminToken, codes.OK)
			later := laterCreated.GetConnection()
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: later.Id}, &pb.VerifyClientResponse{}, everything.Token, codes.OK)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: later.Id}, &pb.IssueTokenResponse{}, everything.Token, codes.PermissionDenied)
			invoke("DeleteConnection", &pb.DeleteConnectionRequest{Id: later.Id}, &pb.DeleteConnectionResponse{}, adminToken, codes.OK)
			emptyScope := &pb.CreateClientResponse{}
			invoke("CreateClient", &pb.CreateClientRequest{CanIssueTokens: true}, emptyScope, adminToken, codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.VerifyClientResponse{}, emptyScope.Token, codes.PermissionDenied)
			tokenReply := &pb.IssueTokenResponse{}
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, tokenReply, issued.Token, codes.OK)
			if tokenReply.Token != forgeToken ||
				tokenReply.Kind != "github" || tokenReply.BaseUrl != forge.URL || tokenReply.GitBaseUrl != forge.URL+"/git" {
				t.Fatal("issued wrong credential")
			}

			invoke(
				"RotateCredential",
				&pb.RotateCredentialRequest{Id: conn.Id, Credential: "rejected-" + forgeToken},
				&pb.RotateCredentialResponse{},
				adminToken,
				codes.InvalidArgument,
			)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, tokenReply, issued.Token, codes.OK)
			if tokenReply.Token != forgeToken {
				t.Fatal("failed rotation changed credential")
			}

			invoke("RotateCredential", &pb.RotateCredentialRequest{Id: conn.Id, Credential: rotatedToken}, &pb.RotateCredentialResponse{}, adminToken, codes.OK)
			invoke("TestConnection", &pb.TestConnectionRequest{Id: conn.Id}, &pb.TestConnectionResponse{}, adminToken, codes.OK)
			revoked.Store(true)
			invoke("TestConnection", &pb.TestConnectionRequest{Id: conn.Id}, &pb.TestConnectionResponse{}, adminToken, codes.InvalidArgument)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.VerifyClientResponse{}, issued.Token, codes.FailedPrecondition)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, issued.Token, codes.FailedPrecondition)
			revoked.Store(false)
			invoke("TestConnection", &pb.TestConnectionRequest{Id: conn.Id}, &pb.TestConnectionResponse{}, adminToken, codes.OK)
			for _, rpc := range []struct {
				name          string
				input, output proto.Message
			}{
				{"ListForges", &pb.ListForgesRequest{}, &pb.ListForgesResponse{}},
				{"ListConnections", &pb.ListConnectionsRequest{}, &pb.ListConnectionsResponse{}},
				{"ListClients", &pb.ListClientsRequest{}, &pb.ListClientsResponse{}},
			} {
				invoke(rpc.name, rpc.input, rpc.output, adminToken, codes.OK)
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

			invoke("GetConnection", &pb.GetConnectionRequest{Id: conn.Id}, &pb.GetConnectionResponse{}, adminToken, codes.OK)
			// Admin credentials are reread, never persisted.
			if err := os.WriteFile(adminFile, []byte("changed-admin"), 0600); err != nil {
				t.Fatal(err)
			}

			invoke("ListClients", &pb.ListClientsRequest{}, &pb.ListClientsResponse{}, adminToken, codes.Unauthenticated)
			invoke("DeleteClient", &pb.DeleteClientRequest{Id: issued.Client.Id}, &pb.DeleteClientResponse{}, "changed-admin", codes.OK)
			invoke("IssueToken", &pb.IssueTokenRequest{ConnectionId: conn.Id}, &pb.IssueTokenResponse{}, issued.Token, codes.Unauthenticated)
			invoke("DeleteForge", &pb.DeleteForgeRequest{Id: f.Id}, &pb.DeleteForgeResponse{}, "changed-admin", codes.FailedPrecondition)
			invoke("DeleteConnection", &pb.DeleteConnectionRequest{Id: conn.Id}, &pb.DeleteConnectionResponse{}, "changed-admin", codes.OK)
			invoke("VerifyClient", &pb.VerifyClientRequest{ConnectionId: conn.Id}, &pb.VerifyClientResponse{}, verifyOnly.Token, codes.PermissionDenied)
			invoke("DeleteForge", &pb.DeleteForgeRequest{Id: f.Id}, &pb.DeleteForgeResponse{}, "changed-admin", codes.OK)
		})
	}
}

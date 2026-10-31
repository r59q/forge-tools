package main

import (
	"context"
	"net"
	"strings"
	"testing"

	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeBroker answers the list calls when given the admin token.
type fakeBroker struct {
	pb.UnimplementedBrokerServer
}

func authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) != 1 || v[0] != "Bearer admin" {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return nil
}

func (fakeBroker) ListForges(ctx context.Context, _ *emptypb.Empty) (*pb.ListForgesResponse, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	return &pb.ListForgesResponse{Forges: []*pb.Forge{{Id: "f1", Kind: "github", BaseUrl: "https://api.github.com"}}}, nil
}

func (fakeBroker) ListConnections(ctx context.Context, _ *emptypb.Empty) (*pb.ListConnectionsResponse, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	return &pb.ListConnectionsResponse{Connections: []*pb.Connection{{Id: "c1", Name: "me", Status: "ok"}}}, nil
}

func (fakeBroker) ListClients(ctx context.Context, _ *emptypb.Empty) (*pb.ListClientsResponse, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	return &pb.ListClientsResponse{Clients: []*pb.Client{{Id: "k1", Name: "proxy", ConnectionIds: []string{"c1"}, CanIssueTokens: true}}}, nil
}

func dial(t *testing.T) pb.BrokerClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterBrokerServer(srv, fakeBroker{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewBrokerClient(conn)
}

func TestRunLists(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), dial(t), "admin", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"f1  github", "c1  me  ok", "k1  proxy  connections=[c1] issue-tokens=true"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}
}

func TestRunWrongToken(t *testing.T) {
	err := run(context.Background(), dial(t), "wrong", &strings.Builder{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

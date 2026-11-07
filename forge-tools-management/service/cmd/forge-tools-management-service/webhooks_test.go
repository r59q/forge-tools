package main

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	reactorapi "github.com/r59q/forge-tools/forge-reactor/service/api"
	reactorpb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// startReactor serves the real forge-reactor API with in-memory state.
func startReactor(t *testing.T) string {
	t.Helper()
	r, err := reactor.New(reactor.Config{Store: &reactor.JSONStore{}})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := sources.New(&sources.Manager{Store: &sources.MemoryStore{}, PublicURL: "https://example.org/reactor"})
	if err != nil {
		t.Fatal(err)
	}
	src, err := reactorapi.NewSources(context.Background(), nil, managed)
	if err != nil {
		t.Fatal(err)
	}
	service, err := reactorapi.New(r, src, forgeauth.StaticToken("reactor-synthetic"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	reactorpb.RegisterReactorServiceServer(g, service)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)

	return l.Addr().String()
}

func managementClient(t *testing.T, address net.Addr) (pb.ManagementServiceClient, context.Context) {
	t.Helper()
	conn, err := grpc.NewClient(address.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	return pb.NewManagementServiceClient(conn), metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer operator-synthetic")
}

func TestRunWithReactor(t *testing.T) {
	t.Setenv("MANAGEMENT_REACTOR_TEST_TOKEN", "reactor-synthetic")
	s, err := parse(
		[]string{
			"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0",
			"--reactor-address=" + startReactor(t), "--reactor-insecure=true", "--reactor-token-env=MANAGEMENT_REACTOR_TEST_TOKEN",
		},
		func(string) string { return "" },
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}

	a := startManagement(t, s)
	client, ctx := managementClient(t, a[0])
	added, err := client.AddWebhook(ctx, &pb.AddWebhookRequest{Id: "app", Forge: "forgejo", Repositories: []string{"team/app"}})
	if err != nil || len(added.GetSecret()) != 64 || added.GetWebhook().GetUrl() != "https://example.org/reactor/webhooks/app" {
		t.Fatalf("add = %v, %v", added, err)
	}
	if _, err := client.AddWebhook(ctx, &pb.AddWebhookRequest{Id: "app", Forge: "forgejo"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := client.AddWebhook(ctx, &pb.AddWebhookRequest{
		Id: "reg", Forge: "github", Connection: "gh", Repositories: []string{"a/b"}, Register: true,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("register without connector = %v", err)
	}

	code, body := httpGet(t, a[1], "/webhooks", true)
	if code != 200 || !strings.Contains(body, "team/app") || strings.Contains(body, added.GetSecret()) {
		t.Fatalf("page: %d %s", code, body)
	}

	rotated, err := client.RotateWebhookSecret(ctx, &pb.RotateWebhookSecretRequest{Id: "app"})
	if err != nil || rotated.GetSecret() == added.GetSecret() {
		t.Fatalf("rotate = %v, %v", rotated, err)
	}
	if _, err := client.RemoveWebhook(ctx, &pb.RemoveWebhookRequest{Id: "app"}); err != nil {
		t.Fatal(err)
	}
	if list, err := client.ListWebhooks(ctx, &pb.ListWebhooksRequest{}); err != nil || !list.GetConfigured() || len(list.GetWebhooks()) != 0 {
		t.Fatalf("list = %v, %v", list, err)
	}

	t.Setenv("MANAGEMENT_REACTOR_TEST_TOKEN", "wrong-synthetic")
	if _, err := client.ListWebhooks(ctx, &pb.ListWebhooksRequest{}); status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("rejected token = %v", err)
	}
}

func TestReactorSettings(t *testing.T) {
	for _, args := range [][]string{
		{"--reactor-insecure=true"},
		{"--reactor-token-env=TOKEN"},
		{"--reactor-address=localhost:9103", "--reactor-token-file=f", "--reactor-token-env=TOKEN"},
		{"--reactor-address=localhost:9103", "--reactor-insecure=bad"},
	} {
		if _, err := parse(args, func(string) string { return "" }, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}

	s, err := parse([]string{"--reactor-address=localhost:9103"}, func(string) string { return "" }, io.Discard)
	if err != nil || s.reactorTokenEnv != "FORGE_MANAGEMENT_REACTOR_TOKEN" {
		t.Fatalf("%+v %v", s, err)
	}
}

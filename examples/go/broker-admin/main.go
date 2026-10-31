// Command broker-admin lists the forges, connections and clients stored in a
// forge-connector broker, using its gRPC API.
//
//	go run ./broker-admin -addr 127.0.0.1:9090
//
// The admin token is read from FORGE_BROKER_ADMIN_TOKEN.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "broker gRPC address")
	flag.Parse()
	token := os.Getenv("FORGE_BROKER_ADMIN_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "FORGE_BROKER_ADMIN_TOKEN is not set")
		os.Exit(1)
	}
	// Plaintext suits a local broker; use TLS credentials for remote ones.
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, pb.NewBrokerClient(conn), token, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, client pb.BrokerClient, token string, out io.Writer) error {
	if token == "" {
		return errors.New("admin token required")
	}
	// The broker reads the bearer token from the authorization metadata.
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	forges, err := client.ListForges(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	for _, f := range forges.GetForges() {
		fmt.Fprintf(out, "forge      %s  %s  %s\n", f.GetId(), f.GetKind(), f.GetBaseUrl())
	}
	conns, err := client.ListConnections(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	for _, c := range conns.GetConnections() {
		fmt.Fprintf(out, "connection %s  %s  %s\n", c.GetId(), c.GetName(), c.GetStatus())
	}
	clients, err := client.ListClients(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	for _, c := range clients.GetClients() {
		fmt.Fprintf(out, "client     %s  %s  connections=%v issue-tokens=%t\n",
			c.GetId(), c.GetName(), c.GetConnectionIds(), c.GetCanIssueTokens())
	}
	return nil
}

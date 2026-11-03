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
)

func main() {
	if err := start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// start reads the flags and token, then prints the broker's contents.
func start() error {
	addr := flag.String("addr", "127.0.0.1:9090", "broker gRPC address")
	flag.Parse()
	token := os.Getenv("FORGE_BROKER_ADMIN_TOKEN")
	if token == "" {
		return errors.New("FORGE_BROKER_ADMIN_TOKEN is not set")
	}

	// Plaintext suits a local broker; use TLS credentials for remote ones.
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}

	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return run(ctx, pb.NewBrokerServiceClient(conn), token, os.Stdout)
}

func run(ctx context.Context, client pb.BrokerServiceClient, token string, out io.Writer) error {
	if token == "" {
		return errors.New("admin token required")
	}

	// The broker reads the bearer token from the authorization metadata.
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	forges, err := client.ListForges(ctx, &pb.ListForgesRequest{})
	if err != nil {
		return err
	}

	for _, f := range forges.GetForges() {
		fmt.Fprintf(out, "forge      %s  %s  %s\n", f.GetId(), f.GetKind(), f.GetBaseUrl())
	}

	conns, err := client.ListConnections(ctx, &pb.ListConnectionsRequest{})
	if err != nil {
		return err
	}

	for _, c := range conns.GetConnections() {
		fmt.Fprintf(out, "connection %s  %s  %s\n", c.GetId(), c.GetName(), c.GetStatus())
	}

	clients, err := client.ListClients(ctx, &pb.ListClientsRequest{})
	if err != nil {
		return err
	}

	for _, c := range clients.GetClients() {
		fmt.Fprintf(out, "client     %s  %s  connections=%v issue-tokens=%t\n",
			c.GetId(), c.GetName(), c.GetConnectionIds(), c.GetCanIssueTokens())
	}

	return nil
}

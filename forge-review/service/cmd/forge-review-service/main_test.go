package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/servicekit"
)

func TestParseStandaloneAndFlags(t *testing.T) {
	s, err := parse(nil, func(string) string { return "" }, io.Discard)
	if err != nil || s.ConnectorURL != "" || s.TokenEnv != "FORGE_REVIEW_TOKEN" || s.dataDirectory != "./review-data" {
		t.Fatal(s, err)
	}

	for _, args := range [][]string{{"--data-dir="}, {"--request-timeout=0s"}, {"--token-env=X", "--token-file=x"}, {"--connector-token-env=X", "--connector-token-file=x"}, {"--unknown"}} {
		if _, err := parse(args, func(string) string { return "" }, io.Discard); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

func TestServiceRestartPreservesReviews(t *testing.T) {
	t.Setenv("REVIEW_TEST_TOKEN", "fixture-token")
	s := settings{
		Settings: servicekit.Settings{
			GRPCAddress: "127.0.0.1:0",
			HTTPAddress: "127.0.0.1:0",
			TokenEnv:    "REVIEW_TEST_TOKEN",
			Timeout:     time.Second,
		},
		dataDirectory: t.TempDir(),
	}
	start := func() (string, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan string, 1)
		done := make(chan error, 1)
		go func() { done <- run(ctx, s, func(_, httpAddr net.Addr) { ready <- "http://" + httpAddr.String() }) }()
		var address string
		select {
		case address = <-ready:
		case err := <-done:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("startup timed out")
		}

		stop := func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(7 * time.Second):
				t.Error("shutdown timed out")
			}
		}

		return address, stop
	}
	request := func(base, method, path, body string) map[string]any {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer fixture-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			data, _ := io.ReadAll(resp.Body)
			t.Fatalf("%d %s", resp.StatusCode, data)
		}

		var out map[string]any
		if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}

		return out
	}
	base, stop := start()
	subject := request(base, "POST", "/v1/subjects", `{"kind":"patch","patch":"--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"}`)
	review := request(base, "POST", "/v1/reviews", `{"subjectId":"`+subject["id"].(string)+`","reviewer":"alice"}`)
	id := review["id"].(string)
	request(base, "POST", "/v1/reviews/"+id+"/submission", `{"status":"approved"}`)
	stop()
	base, stop = start()
	defer stop()
	got := request(base, "GET", "/v1/reviews/"+id, "")
	if got["status"] != "approved" || got["subjectId"] != subject["id"] {
		t.Fatal(got)
	}
}

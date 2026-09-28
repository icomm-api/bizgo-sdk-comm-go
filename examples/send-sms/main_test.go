package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/icomm-api/bizgo-sdk-comm-go"
	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

func TestRun(t *testing.T) {
	srv := ts.New(t)
	route := srv.On("POST", "/api/comm/v1/send/omni", ts.OK(map[string]any{
		"destinations": []any{map[string]any{"to": "01000000000", "msgKey": "K001", "code": "A000", "result": "Success"}}}))
	client, err := bizgo.NewClient(bizgo.WithAPIKey(ts.APIKey), bizgo.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), client, "01000000000", "01000000000", &out); err != nil {
		t.Fatal(err)
	}
	if route.Count() != 1 || !strings.Contains(out.String(), "[K001]") {
		t.Fatalf("calls=%d out=%s", route.Count(), out.String())
	}
}

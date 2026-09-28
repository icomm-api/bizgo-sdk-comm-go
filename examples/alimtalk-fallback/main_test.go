package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icomm-api/bizgo-sdk-comm-go"
	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

var cfg = config{from: "01000000000", to: "01000000000", senderKey: "SENDER_KEY_EXAMPLE", templateCode: "TEMPLATE_CODE_EXAMPLE"}

func newClient(t *testing.T, srv *ts.Server) *bizgo.Client {
	t.Helper()
	client, err := bizgo.NewClient(bizgo.WithAPIKey(ts.APIKey), bizgo.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRun(t *testing.T) {
	srv := ts.New(t)
	route := srv.On("POST", "/api/comm/v1/send/omni", ts.OK(map[string]any{
		"destinations": []any{map[string]any{"to": "01000000000", "msgKey": "K001", "code": "A000"}}}))
	var out bytes.Buffer
	if err := run(context.Background(), newClient(t, srv), cfg, &out); err != nil {
		t.Fatal(err)
	}
	var body struct {
		MessageFlow    []map[string]json.RawMessage `json:"messageFlow"`
		IdempotencyKey string                       `json:"idempotencyKey"`
	}
	if err := json.Unmarshal(route.Last(t).Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.MessageFlow) != 2 || body.MessageFlow[0]["alimtalk"] == nil || body.MessageFlow[1]["sms"] == nil {
		t.Fatalf("flow = %v", body.MessageFlow)
	}
	if body.IdempotencyKey == "" || strings.Contains(body.IdempotencyKey, cfg.to) {
		t.Fatalf("idempotencyKey = %q", body.IdempotencyKey)
	}
}

func TestRunHandlesDuplicates(t *testing.T) {
	srv := ts.New(t)
	srv.On("POST", "/api/comm/v1/send/omni", ts.JSON(200, ts.Envelope(nil, "A301", "")))
	var out bytes.Buffer
	if err := run(context.Background(), newClient(t, srv), cfg, &out); err != nil || !strings.Contains(out.String(), "이미 발송") {
		t.Fatalf("%v %s", err, out.String())
	}
}

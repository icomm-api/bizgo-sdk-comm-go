package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/icomm-api/bizgo-sdk-comm-go"
	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

func TestRun(t *testing.T) {
	srv := ts.New(t)
	srv.On("GET", "/api/comm/v1/report/polling",
		ts.OK(map[string]any{"reportId": "R1", "report": []any{map[string]any{"msgKey": "K001", "reportCode": "10000"}}}),
		ts.OK(map[string]any{"reportId": "", "report": nil}))
	ack := srv.On("DELETE", "/api/comm/v1/report/polling/R1", ts.OK(nil))
	client, err := bizgo.NewClient(bizgo.WithAPIKey(ts.APIKey), bizgo.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	db := store{}
	var out bytes.Buffer
	if err := run(context.Background(), client, db, &out); err != nil {
		t.Fatal(err)
	}
	if db["K001"] != "delivered" || ack.Count() != 1 {
		t.Fatalf("db=%v acks=%d", db, ack.Count())
	}
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/icomm-api/bizgo-sdk-comm-go"
	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

func TestRun(t *testing.T) {
	srv := ts.New(t)
	history := srv.On("GET", "/api/comm/v1/message/history", ts.OK(map[string]any{
		"messages": []any{map[string]any{"msgKey": "K001", "reportCode": "63020"}}, "hasNext": false}))
	status := srv.On("GET", "/api/comm/v1/message/inquiry/msgKey/K001", ts.OK(map[string]any{
		"messages": []any{map[string]any{"msgKey": "K001", "serviceType": "ALIMTALK", "reportCode": "63020"}}}))
	client, err := bizgo.NewClient(bizgo.WithAPIKey(ts.APIKey), bizgo.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	if err := run(context.Background(), client, now, &out); err != nil {
		t.Fatal(err)
	}
	q := history.Last(t).Query
	if status.Count() != 1 || q.Get("requestTime") != "2026-09-23T09:00:00" || q.Get("serviceType") != "SMS,ALIMTALK" ||
		!strings.Contains(out.String(), "ALIMTALK 63020") {
		t.Fatalf("out=%s query=%v", out.String(), q)
	}
}

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

func TestRun(t *testing.T) {
	srv := ts.New(t)
	srv.On("POST", "/api/comm/v1/file/mms", ts.OK(map[string]any{"fileKey": "FILE_KEY_001"}))
	send := srv.On("POST", "/api/comm/v1/send/omni", ts.OK(map[string]any{
		"destinations": []any{map[string]any{"to": "01000000000", "msgKey": "K001", "code": "A000"}}}))
	client, err := bizgo.NewClient(bizgo.WithAPIKey(ts.APIKey), bizgo.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), client, "01000000000", "01000000000", strings.NewReader("\xff\xd8jpeg"), &out); err != nil {
		t.Fatal(err)
	}
	var body struct {
		MessageFlow []struct {
			MMS struct {
				FileKey []string `json:"fileKey"`
			} `json:"mms"`
		} `json:"messageFlow"`
	}
	if err := json.Unmarshal(send.Last(t).Body, &body); err != nil || body.MessageFlow[0].MMS.FileKey[0] != "FILE_KEY_001" {
		t.Fatalf("%v %s", err, send.Last(t).Body)
	}
}

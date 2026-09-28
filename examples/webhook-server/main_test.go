package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func TestWebhookServer(t *testing.T) {
	secret := []byte("test-webhook-secret")
	receiver, err := bizgo.NewWebhookReceiver(secret)
	if err != nil {
		t.Fatal(err)
	}
	s := &seen{keys: map[string]bool{}}
	srv := httptest.NewServer(newMux(receiver, s))
	defer srv.Close()

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	body := `{"msgKey":"K001","serviceType":"SMS","reportTime":"t","reportType":"0","reportCode":"10000"}`

	post := func(signature string) (int, string) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/bizgo/report", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(bizgo.WebhookTimestampHeader, timestamp)
		req.Header.Set(bizgo.WebhookSignatureHeader, signature)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	if code, got := post(hex.EncodeToString(mac.Sum(nil))); code != 200 || got != `{"msgKey":"K001"}` {
		t.Fatalf("%d %s", code, got)
	}
	if code, _ := post(strings.Repeat("0", 64)); code != 401 {
		t.Fatal(code)
	}
	if !s.keys["K001"] || len(s.keys) != 1 {
		t.Fatal(s.keys)
	}
}

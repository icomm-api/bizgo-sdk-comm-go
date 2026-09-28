package bizgo

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

const (
	apiKey     = ts.APIKey
	phone      = "01000000000"
	otherPhone = "01000001234"
)

// delays records the retry waits instead of sleeping, so the suite stays fast.
type delays struct {
	mu sync.Mutex
	d  []time.Duration
}

func (d *delays) sleep(ctx context.Context, v time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.d = append(d.d, v)
	return ctx.Err()
}

func (d *delays) all() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Duration(nil), d.d...)
}

func newTestClient(t *testing.T, opts ...Option) (*Client, *ts.Server, *delays) {
	t.Helper()
	srv := ts.New(t)
	all := append([]Option{WithAPIKey(apiKey), WithBaseURL(srv.URL), WithoutRateLimit()}, opts...)
	c, err := NewClient(all...)
	if err != nil {
		t.Fatal(err)
	}
	d := &delays{}
	c.t.sleep = d.sleep
	return c, srv, d
}

func accepted(codes ...string) ts.Reply {
	var dests []map[string]any
	for i, c := range codes {
		dests = append(dests, map[string]any{"to": phone, "msgKey": "KEY00" + string(rune('0'+i)), "code": c, "result": "r"})
	}
	return ts.JSON(200, ts.Envelope(map[string]any{"destinations": dests}, "", "ref-1"))
}

// jsonEqual compares JSON documents structurally.
func jsonEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}

func mustNotContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Fatalf("%q must not contain %q", s, sub)
		}
	}
}

func sms(to ...string) SMSParams {
	return SMSParams{To: To(to...), From: phone, Text: "x"}
}

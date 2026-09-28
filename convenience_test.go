package bizgo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

func numbers(n int) []bizgo.Destination {
	out := make([]bizgo.Destination, n)
	for i := range out {
		out[i] = bizgo.Destination{To: fmt.Sprintf("010%08d", i)}
	}
	return out
}

var smsMessage = []bizgo.ChannelMessage{&bizgo.SMSMessage{From: "01000000000", Text: "hello"}}

// The cross-SDK test vector of SDK-DESIGN.md §12.3: every SDK must produce exactly these keys.
func TestBulkIdempotencyKeyTestVector(t *testing.T) {
	to := bizgo.To("01000000000", "01000000001", "01000000002")
	if k := bizgo.BulkIdempotencyKey("camp", 2, 0, to[0:2]); k != "camp-2-0-32fe30d2" {
		t.Fatal(k)
	}
	if k := bizgo.BulkIdempotencyKey("camp", 2, 2, to[2:3]); k != "camp-2-2-370752d8" {
		t.Fatal(k)
	}
	fake := bizgotest.New()
	res, err := mustClient(t, fake).Send.Bulk(context.Background(), bizgo.BulkParams{
		To: to, Messages: smsMessage, ChunkSize: 2, Concurrency: 1, IdempotencyKeyPrefix: "camp",
	})
	if err != nil || len(res.Errors) != 0 || len(res.MsgKeys()) != 3 {
		t.Fatal(res, err)
	}
	var keys []string
	for _, r := range fake.Requests() {
		keys = append(keys, r.JSON.(map[string]any)["idempotencyKey"].(string))
	}
	if strings.Join(keys, " ") != "camp-2-0-32fe30d2 camp-2-2-370752d8" {
		t.Fatal(keys)
	}
}

func TestBulkSplitsSendsConcurrentlyAndKeepsGoing(t *testing.T) {
	var inFlight, peak atomic.Int32
	slow := &countingTransport{before: func() {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
	}}
	fake := bizgotest.New()
	slow.next = fake
	// the third request (some chunk) fails; the fifth has a rejected recipient
	fake.On("sendOmni").Default().Default().Fail(bizgo.LayerService, 200, "A306").Default().SendResult("A000", "A306").Default()
	c, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithBaseURL(string(bizgo.Sandbox)),
		bizgo.WithoutRateLimit(), bizgo.WithTrustedHTTPClient(&http.Client{Transport: slow}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Send.Bulk(context.Background(), bizgo.BulkParams{To: numbers(1100), Messages: smsMessage, Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 6 || len(res.Errors) != 1 || peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("results %d, errors %d, peak %d", len(res.Results), len(res.Errors), peak.Load())
	}
	e := res.Errors[0]
	if res.Results[e.Chunk] != nil || e.End-e.Start != 200 || !errors.Is(e, bizgo.ErrBadRequest) {
		t.Fatalf("%+v", e)
	}
	// the error names indexes, never numbers
	if strings.Contains(e.Error(), "010") || !strings.Contains(e.Error(), fmt.Sprintf("수신자 %d~%d", e.Start, e.End-1)) {
		t.Fatal(e.Error())
	}
	accepted := 0
	for _, r := range res.Results {
		if r != nil {
			accepted += len(r.Destinations)
		}
	}
	if len(res.Succeeded()) != accepted-1 || len(res.Failed()) != 1 || len(res.MsgKeys()) != accepted-1 || accepted < 700 {
		t.Fatalf("succeeded %d, failed %d", len(res.Succeeded()), len(res.Failed()))
	}
	sizes := map[int]int{}
	for _, r := range fake.Requests() {
		sizes[len(r.JSON.(map[string]any)["destinations"].([]any))]++
		if _, ok := r.JSON.(map[string]any)["idempotencyKey"]; ok {
			t.Fatal("no prefix, no key")
		}
		if _, ok := r.JSON.(map[string]any)["idempotencyTtl"]; ok {
			t.Fatal("no prefix, no idempotencyTtl")
		}
	}
	if sizes[200] != 5 || sizes[100] != 1 {
		t.Fatal(sizes)
	}
}

// A panic in one chunk (here in the HTTP transport) is a chunk error; the accepted chunks are kept (§12.2).
func TestBulkKeepsAcceptedChunksWhenOneChunkPanics(t *testing.T) {
	fake := bizgotest.New()
	var calls atomic.Int32
	tr := &countingTransport{next: fake, before: func() {
		if calls.Add(1) == 2 {
			panic("boom")
		}
	}}
	c, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithoutRateLimit(), bizgo.WithTrustedHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Send.Bulk(context.Background(), bizgo.BulkParams{To: numbers(3), Messages: smsMessage, ChunkSize: 1, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !errors.Is(res.Errors[0].Err, bizgo.ErrChunkPanic) || res.Errors[0].Chunk != 1 || len(res.MsgKeys()) != 2 {
		t.Fatalf("%+v %v", res.Errors, res.MsgKeys())
	}
}

func TestBulkValidatesEveryChunkBeforeSending(t *testing.T) {
	fake := bizgotest.New()
	c := mustClient(t, fake)
	to := numbers(450)
	to[420].To = ""
	_, err := c.Send.Bulk(context.Background(), bizgo.BulkParams{To: to, Messages: smsMessage})
	var ve *bizgo.ValidationError
	if !errors.As(err, &ve) || ve.Problems[0].Path != "destinations[420].to" || len(fake.Requests()) != 0 {
		t.Fatal(err)
	}
	for _, p := range []bizgo.BulkParams{
		{To: numbers(1), Messages: smsMessage, ChunkSize: 201},
		{To: numbers(1), Messages: smsMessage, Concurrency: -1},
		{To: nil, Messages: smsMessage},
		{To: numbers(1)},
		{To: numbers(1), Messages: smsMessage, IdempotencyKeyPrefix: strings.Repeat("p", 190)},
	} {
		if _, err := c.Send.Bulk(context.Background(), p); !errors.Is(err, bizgo.ErrValidation) {
			t.Fatalf("%+v: %v", p.ChunkSize, err)
		}
	}
}

func TestBulkWithCanceledContextSendsNothing(t *testing.T) {
	fake := bizgotest.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := mustClient(t, fake).Send.Bulk(ctx, bizgo.BulkParams{To: numbers(3), Messages: smsMessage, ChunkSize: 1})
	if err != nil || len(res.Errors) != 3 || len(fake.Requests()) != 0 || !errors.Is(res.Errors[0], bizgo.ErrConnection) {
		t.Fatal(res, err)
	}
}

type countingTransport struct {
	next   http.RoundTripper
	before func()
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.before()
	return c.next.RoundTrip(r)
}

// ---- hooks (§11.4) ----

type hookKey struct{}

type recorder struct {
	mu     sync.Mutex
	events []bizgo.RequestEvent
}

func (r *recorder) hooks() bizgo.Hooks {
	return bizgo.Hooks{
		OnRequestStart: func(ctx context.Context, e bizgo.RequestEvent) context.Context {
			return context.WithValue(ctx, hookKey{}, e.OperationID)
		},
		OnRequestEnd: func(ctx context.Context, e bizgo.RequestEvent) {
			if ctx.Value(hookKey{}) != e.OperationID {
				panic("the start context is not passed to the end hook")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, e)
		},
	}
}

func TestHooksSeeOperationsNeverValues(t *testing.T) {
	fake := bizgotest.New()
	rec := &recorder{}
	c := mustClient(t, fake, bizgo.WithHooks(rec.hooks()))
	ctx := context.Background()
	fake.On("getMoHistory").Fail(bizgo.LayerGateway, 503, "").Default()
	if _, err := c.Messages.MOHistory(ctx, bizgo.MOHistoryParams{OccurredTime: time.Now(), From: "01000001234"}); err != nil {
		t.Fatal(err)
	}
	fake.On("getReportInquiry").Fail(bizgo.LayerService, 200, "A020")
	_, _ = c.Reports.Inquiry(ctx, "MSGKEY-SECRET-VALUE")
	fake.On("getMessageStatistics").Respond(200, bizgotest.SuccessEnvelope(map[string]any{"statistics": "not-a-list"}, nil))
	_, _ = c.Messages.Statistics(ctx, bizgo.StatisticsParams{StartDate: time.Now()})

	if len(rec.events) != 3 {
		t.Fatalf("%d events", len(rec.events))
	}
	mo, inquiry, stats := rec.events[0], rec.events[1], rec.events[2]
	if mo.OperationID != "getMoHistory" || mo.Operation != "messages.moHistory" || mo.Method != http.MethodGet ||
		mo.PathTemplate != "/api/comm/v1/message/history/mo" || mo.Status != 200 || !mo.Success || mo.Attempts != 2 || mo.Duration < 0 || mo.Started.IsZero() {
		t.Fatalf("%+v", mo)
	}
	if inquiry.PathTemplate != "/api/comm/v1/report/inquiry/{msgKey}" || inquiry.Success || inquiry.Layer != bizgo.LayerService ||
		inquiry.Code != "A020" || inquiry.ErrorKind != "rate limit" {
		t.Fatalf("%+v", inquiry)
	}
	// a response that does not decode is never reported as a success (§12.2)
	if stats.Success || stats.ErrorKind != "invalid response" || stats.Status != 200 {
		t.Fatalf("%+v", stats)
	}
	dump := fmt.Sprintf("%+v", rec.events)
	for _, secret := range []string{"01000001234", "MSGKEY-SECRET-VALUE", bizgotest.FakeAPIKey, "occurredTime"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("hook event contains %q", secret)
		}
	}
}

// A panicking hook (§12.15) changes nothing: not the result, not the retries, not a bulk send.
func TestPanickingHooksAreIgnored(t *testing.T) {
	fake := bizgotest.New()
	bad := bizgo.Hooks{
		OnRequestStart: func(context.Context, bizgo.RequestEvent) context.Context { panic("start") },
		OnRequestEnd:   func(context.Context, bizgo.RequestEvent) { panic("end") },
	}
	rec := &recorder{}
	c := mustClient(t, fake, bizgo.WithHooks(bad, rec.hooks()))
	res, err := c.Send.Bulk(context.Background(), bizgo.BulkParams{To: numbers(5), Messages: smsMessage, ChunkSize: 2})
	if err != nil || len(res.Errors) != 0 || len(res.MsgKeys()) != 5 || len(rec.events) != 3 {
		t.Fatal(res, err, len(rec.events))
	}
}

// ---- rate limit (§11.2) ----

// Recipients, not requests, are counted in the send bucket; the other bucket counts requests.
func TestRateLimitIsAppliedPerRecipient(t *testing.T) {
	fake := bizgotest.New()
	c, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithRateLimit(100, 50),
		bizgo.WithHTTPClient(&http.Client{Transport: fake}))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// 100 tokens at start: 60 + 60 recipients need 20 more tokens = 0.2s at 100/s
	for range 2 {
		if _, err := c.Send.Omni(context.Background(), bizgo.OmniParams{To: numbers(60), Messages: smsMessage}); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	// other requests use their own bucket (50 at start: no wait for a few)
	start = time.Now()
	for range 5 {
		if _, err := c.Reports.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("other bucket waited %v", d)
	}
	for _, bad := range [][2]float64{{0, 5}, {200, -1}} {
		if _, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithRateLimit(bad[0], bad[1])); !errors.Is(err, bizgo.ErrConfiguration) {
			t.Fatal(bad, err)
		}
	}
}

// ---- bizgotest ----

func TestFakeRecordsWithoutHeaderValues(t *testing.T) {
	fake := bizgotest.New()
	c := mustClient(t, fake)
	if _, err := c.Send.SMS(context.Background(), bizgo.SMSParams{To: bizgo.To("01000000000"), From: "01000000000", Text: "hi", Ref: "r1"}); err != nil {
		t.Fatal(err)
	}
	r := fake.LastRequest()
	if r.OperationID != "sendOmni" || r.Operation != "send.omni" || r.JSON.(map[string]any)["ref"] != "r1" {
		t.Fatalf("%+v", r)
	}
	dump, _ := json.Marshal(fake.Requests())
	if strings.Contains(string(dump), bizgotest.FakeAPIKey) || !strings.Contains(strings.Join(r.HeaderNames, ","), "Authorization") {
		t.Fatal(string(dump))
	}
	if len(fake.RequestsFor("sendOmni")) != 1 || fmt.Sprint(r) != "bizgotest.Request(POST /api/comm/v1/send/omni)" {
		t.Fatal(fmt.Sprint(r))
	}
	// default create reservation answer carries a resvKey
	res, err := c.Reservations.Create(context.Background(), &bizgo.ReservationCreateRequest{
		Destinations: bizgo.To("01000000000"), ResvSendTime: "2026-05-01 10:00:00",
		MessageFlow: []bizgo.ReservationMessageFlowItem{{SMS: &bizgo.SMSMessage{From: "01000000000", Text: "hi"}}},
	})
	if err != nil || res.ResvKey == "" || len(res.Data.Destinations) != 1 {
		t.Fatal(res, err)
	}
	// errors: network, timeout, unknown operation
	fake.On("getReportPolling").NetworkError().Timeout()
	if _, err := c.Reports.Poll(context.Background()); !errors.Is(err, bizgo.ErrConnection) {
		t.Fatal(err)
	}
	if _, err := c.Reports.Poll(context.Background()); !errors.Is(err, bizgo.ErrTimeout) {
		t.Fatal(err)
	}
	fake.Reset()
	if len(fake.Requests()) != 0 {
		t.Fatal("not reset")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown operation must panic")
		}
	}()
	fake.On("noSuchOperation")
}

func TestSignWebhookIsAcceptedByTheReceiver(t *testing.T) {
	secret := []byte("test-webhook-secret")
	receiver, err := bizgo.NewWebhookReceiver(secret)
	if err != nil {
		t.Fatal(err)
	}
	h, body, err := bizgotest.SignWebhook(secret, map[string]any{"msgKey": "KEY001", "code": "A000"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := receiver.Report(h, body)
	if err != nil || report.MsgKey != "KEY001" {
		t.Fatal(report, err)
	}
	stamp := h.Get(bizgo.WebhookTimestampHeader)
	if err := bizgo.VerifyWebhookSignature(secret, stamp, bizgotest.SignatureBase64(secret, stamp), bizgo.DefaultWebhookTolerance, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Every chunk with a generated key gets DefaultIdempotencyTTL unless BulkParams.IdempotencyTTL is set (0 included).
// Rerunning a bulk send with the same keys: per-recipient A301 is aggregated into Duplicates, and a
// chunk whose recipients are all duplicates is not an error. A request-level A301 still is.
func TestBulkAggregatesDuplicates(t *testing.T) {
	fake := bizgotest.New()
	fake.On("sendOmni").SendResult("A301", "A301").SendResult("A000", "A301").Fail(bizgo.LayerService, 200, "A301")
	p := bizgo.BulkParams{To: numbers(5), Messages: smsMessage, ChunkSize: 2, Concurrency: 1, IdempotencyKeyPrefix: "camp"}
	res, err := mustClient(t, fake).Send.Bulk(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Duplicates()) != 3 || len(res.Succeeded()) != 1 || len(res.Failed()) != 0 || len(res.MsgKeys()) != 1 {
		t.Fatalf("duplicates %d, succeeded %d, failed %d", len(res.Duplicates()), len(res.Succeeded()), len(res.Failed()))
	}
	if res.Results[0] == nil || res.Results[1] == nil || len(res.Errors) != 1 || res.Errors[0].Chunk != 2 ||
		!errors.Is(res.Errors[0], bizgo.ErrDuplicateRequest) {
		t.Fatalf("errors %+v", res.Errors)
	}
	if text := res.LogValue().String(); strings.Contains(text, "01000000000") || strings.Contains(text, "01000000001") {
		t.Fatalf("not masked: %s", text)
	}
}

func TestBulkChunksSendIdempotencyTTL(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  *int
		want float64
	}{{"default", nil, bizgo.DefaultIdempotencyTTL}, {"explicit", bizgo.Ptr(3600), 3600}, {"zero", bizgo.Ptr(0), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			fake := bizgotest.New()
			p := bizgo.BulkParams{To: numbers(5), Messages: smsMessage, ChunkSize: 2, Concurrency: 1, IdempotencyKeyPrefix: "camp", IdempotencyTTL: tc.ttl}
			res, err := mustClient(t, fake).Send.Bulk(context.Background(), p)
			if err != nil || len(res.Errors) != 0 {
				t.Fatal(res, err)
			}
			if reqs := fake.Requests(); len(reqs) != 3 {
				t.Fatal(len(reqs))
			}
			for _, r := range fake.Requests() {
				if v := r.JSON.(map[string]any)["idempotencyTtl"]; v != tc.want {
					t.Fatalf("idempotencyTtl = %v", v)
				}
			}
			if p.IdempotencyTTL != tc.ttl {
				t.Fatal("params modified")
			}
		})
	}
}

package bizgo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

// Regression tests for the hardening rules of SDK-DESIGN.md §12 (numbers in the test names), the
// identification headers of §2.1, the rate limiter of §11.2 and the counsel webhooks of §11.5.

const sendRoutePath = "/api/comm/v1/send/omni"

// §12.1: a response that does not decode is an InvalidResponseError with the status, the tracking
// ID and the body; for a send it says the request may have been accepted.
func TestR01_ParseFailureIsInvalidResponseWithDetails(t *testing.T) {
	c, srv, _ := newTestClient(t)
	body := `{"common":{"authCode":"A000","authResult":"Success","infobankTrId":"TR-1"},"data":{"code":"A000","result":"Success","data":{"destinations":"oops"}}}`
	srv.On("POST", sendRoutePath, ts.Text(200, body))
	_, err := c.Send.SMS(context.Background(), sms(phone))
	var ie *InvalidResponseError
	if !errors.As(err, &ie) || ie.HTTPStatus != 200 || ie.TrackingID != "TR-1" || string(ie.Body) != body {
		t.Fatalf("%#v", err)
	}
	mustContain(t, err.Error(), "접수됐을 수 있으니")
	mustNotContain(t, fmt.Sprintf("%#v", ie), "oops")
	// a read does not say that
	srv.On("GET", statsRoute, ts.Text(200, `{"common":{"authCode":"A000"},"data":{"code":"A000","data":{"statistics":1}}}`))
	if err := stats(c); !errors.Is(err, ErrInvalidResponse) || strings.Contains(err.Error(), "접수") {
		t.Fatal(err)
	}
}

// §12.4: A301 after any retry (5xx or 429, not only connection errors) is AlreadyAccepted.
func TestR04_DuplicateAfterAnyRetryIsAlreadyAccepted(t *testing.T) {
	dup := ts.JSON(200, ts.Envelope(nil, "A301", ""))
	for _, first := range []ts.Reply{ts.Text(503, "busy"), ts.JSON(429, map[string]any{"common": map[string]any{"authCode": "A020"}})} {
		c, srv, _ := newTestClient(t)
		srv.On("POST", sendRoutePath, first, dup)
		p := sms(phone)
		p.IdempotencyKey = "order-1"
		_, err := c.Send.SMS(context.Background(), p)
		var ae *APIError
		if !errors.As(err, &ae) || !errors.Is(err, ErrDuplicateRequest) || !ae.AlreadyAccepted {
			t.Fatalf("%#v", err)
		}
		mustContain(t, err.Error(), "이미 접수")
	}
	// on the first attempt it is a plain duplicate
	c, srv, _ := newTestClient(t)
	srv.On("POST", sendRoutePath, dup)
	_, err := c.Send.SMS(context.Background(), sms(phone))
	var ae *APIError
	if !errors.As(err, &ae) || ae.AlreadyAccepted {
		t.Fatalf("%#v", err)
	}
}

// §12.5: "." and ".." are rejected, everything else is one escaped segment.
func TestR05_PathValues(t *testing.T) {
	for _, bad := range []string{".", "..", ""} {
		if _, err := buildPath("/a/{x}/b", bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	p, err := buildPath("/a/{x}/b/{y}", "../etc", "%2e%2e")
	if err != nil || p != "/a/..%2Fetc/b/%252e%252e" {
		t.Fatal(p, err)
	}
}

// §12.6: the Authorization header cannot be replaced, redirects are refused (also with a client
// that would follow them), cookies are not kept, and base URLs with user info, query or fragment are
// rejected.
type headerSetter struct{ next http.RoundTripper }

func (h headerSetter) RoundTrip(r *http.Request) (*http.Response, error) {
	// a well-behaved wrapper sees the SDK headers already set
	if r.Header.Get("Authorization") != apiKey || r.Header.Get("X-Bizgo-Client") != sdkClient {
		return nil, errors.New("SDK headers missing")
	}
	return h.next.RoundTrip(r)
}

func TestR06_AuthorizationStaysWithTheBaseURL(t *testing.T) {
	other := ts.New(t)
	stolen := other.On("GET", statsRoute, ts.OK(nil))
	c, srv, _ := newTestClient(t, WithTrustedHTTPClient(&http.Client{Transport: headerSetter{http.DefaultTransport}}))
	redirect := ts.Text(307, "")
	redirect.Header = http.Header{"Location": {other.URL + statsRoute}}
	route := srv.On("GET", statsRoute, redirect)
	err := stats(c)
	if !errors.Is(err, ErrInvalidResponse) || stolen.Count() != 0 || route.Count() != 1 {
		t.Fatalf("%v, stolen %d, calls %d", err, stolen.Count(), route.Count())
	}
	mustContain(t, err.Error(), "HTTP 307") // §12.12: the status is in the message
	if c.t.http.Jar != nil {
		t.Fatal("cookie jar kept")
	}
	// clients that would follow redirects or keep cookies are refused, trusted or not
	follow := func(*http.Request, []*http.Request) error { return nil }
	jar, _ := cookiejar.New(nil)
	for _, hc := range []*http.Client{{CheckRedirect: follow}, {Jar: jar}} {
		for _, opt := range []Option{WithHTTPClient(hc), WithTrustedHTTPClient(hc)} {
			if _, err := NewClient(WithAPIKey(apiKey), opt); !errors.Is(err, ErrConfiguration) {
				t.Fatal(err)
			}
		}
	}
	for _, bad := range []string{"https://user:pw@mars.ibapi.kr", "https://mars.ibapi.kr?x=1", "https://mars.ibapi.kr#frag", "https://mars.ibapi.kr/#"} {
		if _, err := NewClient(WithAPIKey(apiKey), WithBaseURL(bad)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// §12.7: webhook timestamps are 1-16 ASCII digits; body problems are verification errors (4xx).
func TestR07_WebhookInputDefense(t *testing.T) {
	for _, ts := range []string{"", "12345678901234567", "１２３", "12a", "-1", "+1"} {
		err := VerifyWebhookSignature([]byte(webhookSecret), ts, "00", DefaultWebhookTolerance, webhookNow)
		if !errors.Is(err, ErrWebhook) {
			t.Fatalf("%q: %v", ts, err)
		}
	}
	if err := VerifyWebhookSignature([]byte("   "), timestampMS, "00", DefaultWebhookTolerance, webhookNow); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	if _, err := NewWebhookReceiver([]byte(" \t\n")); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	deep := strings.Repeat(`{"a":`, 65) + "1" + strings.Repeat("}", 65)
	for _, body := range []string{deep, `{"msgKey": 5}`, `{"msgKey":`, `[1]`} {
		if _, err := ParseReportWebhook([]byte(body)); !errors.Is(err, ErrWebhook) {
			t.Fatalf("%.20s: %v", body, err)
		}
		if _, err := ParseCounselMessageWebhook([]byte(body)); !errors.Is(err, ErrWebhook) {
			t.Fatalf("%.20s: %v", body, err)
		}
	}
}

// §12.8: 16MB after decompression, JSON depth 64.
func TestR08_ResponseLimits(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = io.WriteString(w, `{"x":"`+strings.Repeat("a", maxResponseBytes)+`"}`)
	_ = w.Close()
	if gz.Len() > 1<<20 {
		t.Fatal("test body not compressed")
	}
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.Reply{Status: 200, Header: http.Header{"Content-Encoding": {"gzip"}}, Body: gz.String()})
	if err := stats(c); !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "16MB") {
		t.Fatal(err)
	}
	nested := func(n int) string {
		return `{"common":{"authCode":"A000"},"data":{"code":"A000","x":` + strings.Repeat("[", n-2) + strings.Repeat("]", n-2) + `}}`
	}
	srv.On("GET", statsRoute, ts.Text(200, nested(65)))
	if err := stats(c); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
	srv.On("GET", statsRoute, ts.Text(200, nested(64)))
	if err := stats(c); err != nil {
		t.Fatal(err)
	}
	if !jsonDepthOK([]byte(`{"a":"[[[[[[[[[[\"]]"}`), 1) {
		t.Fatal("brackets in strings counted")
	}
}

// §12.9: Retry-After must be a finite number of seconds 0-60.
func TestR09_RetryAfterValues(t *testing.T) {
	for value, want := range map[string]string{"0": "0s", "1.5": "1.5s", "100": "1m0s", "60": "1m0s"} {
		d, ok := parseRetryAfter(http.Header{"Retry-After": {value}})
		if !ok || d.String() != want {
			t.Fatalf("%s: %v %v", value, d, ok)
		}
	}
	for _, value := range []string{"-1", "NaN", "Inf", "+Inf", "1e3", "0x10", "Wed, 21 Oct 2015 07:28:00 GMT", ".", "1.2.3", ""} {
		if _, ok := parseRetryAfter(http.Header{"Retry-After": {value}}); ok {
			t.Fatalf("%q accepted", value)
		}
	}
}

// §12.10: errors survive an encoding/json round trip with their kind.
func TestR10_ErrorsRoundTripThroughJSON(t *testing.T) {
	api := newAPIError(200, "A306", LayerService, "Failed", "TR-1", []byte(`{}`))
	api.AlreadyAccepted, api.RetryAfter = true, 2*time.Second
	cases := []struct {
		err  error
		into error
		kind error
	}{
		{api, &APIError{}, ErrBadRequest},
		{newAPIError(429, "A020", LayerService, "", "", nil), &APIError{}, ErrRateLimit},
		{&ConnectionError{Timeout: true, msg: "요청 시간이 초과되었습니다"}, &ConnectionError{}, ErrTimeout},
		{&InvalidResponseError{HTTPStatus: 200, TrackingID: "TR", Body: []byte("x"), msg: "m"}, &InvalidResponseError{}, ErrInvalidResponse},
		{&ConfigurationError{msg: "m"}, &ConfigurationError{}, ErrConfiguration},
		{&WebhookVerificationError{msg: "m"}, &WebhookVerificationError{}, ErrWebhook},
		{&ValidationError{Problems: []FieldProblem{{"a", "b"}}}, &ValidationError{}, ErrValidation},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.err)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, c.into); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(c.into, c.kind) || c.into.Error() != c.err.Error() {
			t.Fatalf("%s: %v (%s)", data, c.into, c.into.Error())
		}
	}
	var back *APIError
	if !errors.As(cases[0].into, &back) {
		t.Fatal("not an APIError")
	}
	if !back.AlreadyAccepted || back.RetryAfter != 2*time.Second || back.Description == "" || back.TrackingID != "TR-1" {
		t.Fatalf("%#v", back)
	}
}

// §12.11: String/%v/%#v mask phone numbers and show only the length of content.
func TestR11_ModelsMaskPhoneNumbers(t *testing.T) {
	res := &SendResult{Destinations: []SendDestinationResult{{To: "01000001234", MsgKey: "K1", Code: "A000"}}}
	msg := &SMSMessage{From: "0212345678", Text: "비밀 인증번호 123456"}
	dest := Destination{To: "01000001234", ReplaceWords: map[string]string{"name": "홍길동"}}
	mo := MOMessage{From: "01000005678", To: "15880000", Content: "회신 내용"}
	for _, v := range []any{res, *res, msg, dest, mo, []SendDestinationResult{res.Destinations[0]}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(format, v)
			mustNotContain(t, out, "01000001234", "0212345678", "123456", "홍길동", "01000005678", "회신 내용", "15880000")
		}
	}
	mustContain(t, fmt.Sprint(res.Destinations[0]), "010****1234")
	mustContain(t, fmt.Sprint(msg), "text:(14자)")
	if res.Destinations[0].To != "01000001234" || MaskPhone("0212345678") != "021*****78" || MaskPhone("1234") != "****" {
		t.Fatal("masking changed the value")
	}
}

// §12.15: a panicking log handler does not change the result.
type panicHandler struct{ slog.Handler }

func (panicHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (panicHandler) Handle(context.Context, slog.Record) error { panic("log handler") }

func TestR15_PanickingLoggerIsIgnored(t *testing.T) {
	c, srv, _ := newTestClient(t, WithLogger(slog.New(panicHandler{})))
	srv.On("POST", sendRoutePath, accepted("A000"))
	if _, err := c.Send.SMS(context.Background(), sms(phone)); err != nil {
		t.Fatal(err)
	}
}

// §12.16: the key must be printable ASCII without spaces, and is never trimmed.
func TestR16_APIKeyIsNotTrimmed(t *testing.T) {
	for _, bad := range []string{apiKey + "\n", " " + apiKey, "키-not-ascii", "a\x00b", "a\x7fb"} {
		if _, err := NewClient(WithAPIKey(bad)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	c, err := NewClient(WithAPIKey("!~" + apiKey))
	if err != nil || string(c.t.apiKey) != "!~"+apiKey {
		t.Fatal(err)
	}
}

// §12.17: 3xx responses are never retried, even for safe operations.
func TestR17_RedirectsAreNotRetried(t *testing.T) {
	for _, status := range []int{301, 302, 304, 307, 308} {
		c, srv, _ := newTestClient(t)
		reply := ts.Text(status, "")
		if status != 304 {
			reply.Header = http.Header{"Location": {"https://example.invalid/"}}
		}
		route := srv.On("GET", statsRoute, reply)
		err := stats(c)
		if !errors.Is(err, ErrInvalidResponse) || route.Count() != 1 || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
			t.Fatalf("%d: %v, %d calls", status, err, route.Count())
		}
	}
}

// §12.18: an unreadable upload is a ValidationError that names the file, never its full path.
type failingFile struct{}

func (failingFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: `C:\Users\someone\private\photo.jpg`, Err: fs.ErrPermission}
}
func (failingFile) Name() string { return `C:\Users\someone\private\photo.jpg` }

func TestR18_UnreadableUploadFile(t *testing.T) {
	c, _, _ := newTestClient(t)
	_, err := c.Files.UploadMMS(context.Background(), UploadParams{File: failingFile{}})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "photo.jpg") || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	_, err = c.Files.UploadAlimtalkTemplateImage(context.Background(), &AlimtalkTemplateImageUploadRequest{File: &UploadFile{Path: t.TempDir() + "/nope/x.png"}})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "파일이 없습니다: x.png") || strings.Contains(err.Error(), "nope") {
		t.Fatal(err)
	}
}

// §2.1: identification headers, app info, and the SDK headers cannot be replaced.
func TestIdentificationHeaders(t *testing.T) {
	c, srv, _ := newTestClient(t, WithAppInfo("myshop", "1.4.2+build.7"))
	route := srv.On("POST", sendRoutePath, accepted("A000"))
	if _, err := c.Send.SMS(context.Background(), sms(phone)); err != nil {
		t.Fatal(err)
	}
	h := route.Last(t).Header
	ua := h.Get("User-Agent")
	if !strings.HasPrefix(ua, "bizgo-sdk-comm-go/"+Version+" go/") || !strings.HasSuffix(ua, ") app/myshop-1.4.2+build.7") ||
		h.Get("X-Bizgo-Client") != "bizgo-sdk-comm-go/"+Version || h.Get("Authorization") != apiKey {
		t.Fatal(ua, h)
	}
	if !strings.Contains(ua, "("+osName(runtimeGOOS())+"; ") || osName("plan9") != "other" || archName("amd64") != "x64" ||
		archName("386") != "x86" || archName("riscv64") != "other" {
		t.Fatal(ua)
	}
	for _, bad := range [][2]string{{"", "1"}, {"my shop", "1"}, {"a", "1\r\nX: y"}, {strings.Repeat("a", 51), "1"}, {"a", "01000000000@x"}, {"한글", "1"}} {
		if _, err := NewClient(WithAPIKey(apiKey), WithAppInfo(bad[0], bad[1])); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// ---- rate limiter (§11.2, §12.14) with a fake clock ----

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time               { return c.t }
func (c *fakeClock) advance(d time.Duration)      { c.t = c.t.Add(d) }
func (c *fakeClock) sleep(d time.Duration)        { c.advance(d) }
func ms(d time.Duration) int64                    { return d.Milliseconds() }
func waits(l *rateLimiter, b string, c int) int64 { return ms(l.reserve(b, c)) }

func TestTokenBucket(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := newRateLimiter(200, 5, clock.now)
	// starts full: one second of burst
	if waits(l, bucketSend, 200) != 0 {
		t.Fatal("not full at start")
	}
	// the next recipient waits for its token (1/200 s)
	if w := waits(l, bucketSend, 1); w != 5 {
		t.Fatal(w)
	}
	clock.advance(time.Second)
	// cost above the capacity: waits until full, then goes into debt
	l = newRateLimiter(100, 5, clock.now)
	if w := waits(l, bucketSend, 200); w != 0 {
		t.Fatal(w)
	}
	if w := waits(l, bucketSend, 1); w != 1010 { // 100 tokens of debt + 1
		t.Fatal(w)
	}
	// other: 1 token per request whatever the cost; 5 per second
	for range 5 {
		if waits(l, bucketOther, 999) != 0 {
			t.Fatal("other bucket")
		}
	}
	if w := waits(l, bucketOther, 1); w != 200 {
		t.Fatal(w)
	}
	// refill is capped at the capacity
	clock.advance(time.Hour)
	for range 5 {
		if waits(l, bucketOther, 1) != 0 {
			t.Fatal("refill")
		}
	}
	if waits(l, bucketOther, 1) == 0 {
		t.Fatal("capacity exceeded")
	}
}

// Every attempt, retries included, takes its tokens; a 200-recipient send uses the whole second.
func TestRateLimitWaitsBeforeEveryAttempt(t *testing.T) {
	c, srv, d := newTestClient(t, WithRateLimit(200, 5))
	clock := &fakeClock{t: time.Unix(0, 0)}
	c.t.limiter = newRateLimiter(200, 5, clock.now)
	c.t.sleep = func(ctx context.Context, v time.Duration) error { clock.sleep(v); return d.sleep(ctx, v) }
	srv.On("POST", sendRoutePath, accepted(strings.Split(strings.Repeat("A000 ", 200), " ")[:200]...))
	for range 2 {
		if _, err := c.Send.Omni(context.Background(), OmniParams{To: To(make200()...), Messages: []ChannelMessage{&SMSMessage{From: phone, Text: "x"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := fmt.Sprint(d.all()); got != "[1s]" {
		t.Fatal(got)
	}
	// retries of an "other" request wait too
	route := srv.On("GET", statsRoute, ts.Reply{Status: 503, Body: "x", Header: http.Header{"Retry-After": {"0"}}}, ts.OK(map[string]any{}))
	for range 5 {
		_ = stats(c)
	}
	if route.Count() != 6 || len(d.all()) < 3 {
		t.Fatalf("calls %d, sleeps %v", route.Count(), d.all())
	}
}

func make200() []string {
	out := make([]string, 200)
	for i := range out {
		out[i] = phone
	}
	return out
}

// ---- counsel talk webhooks (§11.5) ----

func counselRequest(body string, signed bool, stamp string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cstalk/message", strings.NewReader(body))
	if signed {
		req.Header.Set(WebhookTimestampHeader, stamp)
		req.Header.Set(WebhookSignatureHeader, hex.EncodeToString(sign(stamp, webhookSecret)))
	}
	return req
}

func TestCounselWebhooks(t *testing.T) {
	body := `{"msgKey":"K1","userKey":"USER_KEY_EXAMPLE","senderKey":"SENDER_KEY_EXAMPLE","contents":[{"comment":"안녕하세요"}]}`
	r, _ := NewWebhookReceiver([]byte(webhookSecret))
	r.now = func() time.Time { return webhookNow }
	var got []string
	handler := r.CounselMessageHandler(func(_ context.Context, m *CounselMessageWebhookPayload) error {
		got = append(got, m.MsgKey)
		if m.MsgKey == "FAIL" {
			return errors.New("store failed")
		}
		return nil
	})
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	// no signature headers: parsed and acknowledged with {code, result}
	rec := serve(counselRequest(body, false, ""))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"code":"A000","result":"Success"}` {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// signature headers are ignored, valid or not
	bogus := counselRequest(body, false, "")
	bogus.Header.Set(WebhookTimestampHeader, "not-a-time")
	bogus.Header.Set(WebhookSignatureHeader, "00")
	for _, req := range []*http.Request{counselRequest(body, true, timestampMS), counselRequest(body, true, "1000000000000"), bogus} {
		if rec := serve(req); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	// the body checks still apply: types, depth 64, size; handler errors are 500 (no ack, so Bizgo retries)
	deep := `{"msgKey":"K","contents":` + strings.Repeat("[", 70) + strings.Repeat("]", 70) + `}`
	for _, bad := range []string{`{"msgKey": 1}`, "not json", `[1]`, deep} {
		if rec := serve(counselRequest(bad, false, "")); rec.Code != 400 {
			t.Fatal(bad[:10], rec.Code)
		}
	}
	if rec := serve(counselRequest(`{"msgKey":"`+strings.Repeat("a", MaxWebhookBodyBytes)+`"}`, false, "")); rec.Code != 413 {
		t.Fatal(rec.Code)
	}
	if rec := serve(counselRequest(`{"msgKey":"FAIL"}`, false, "")); rec.Code != 500 || strings.Contains(rec.Body.String(), "A000") {
		t.Fatal(rec.Code)
	}
	if strings.Join(got, ",") != "K1,K1,K1,K1,FAIL" {
		t.Fatal(got)
	}
	// the receiver methods do not look at the headers
	h := http.Header{WebhookSignatureHeader: {"00"}}
	if p, err := r.CounselResult(h, []byte(`{"msgKey":"K"}`)); err != nil || p.MsgKey != "K" {
		t.Fatal(p, err)
	}
	if _, err := r.CounselResult(http.Header{}, []byte(`{"msgKey":1}`)); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	if _, err := r.CounselResult(nil, []byte(deep)); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	if _, err := r.CounselResult(nil, make([]byte, MaxWebhookBodyBytes+1)); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	// report and MO webhooks still require a valid signature
	for _, h := range []http.Header{{}, {WebhookTimestampHeader: {timestampMS}, WebhookSignatureHeader: {"00"}}} {
		if _, err := r.Report(h, webhookBody); !errors.Is(err, ErrWebhook) {
			t.Fatal(err)
		}
		if _, err := r.MO(h, []byte(`{"msgKey":"K"}`)); !errors.Is(err, ErrWebhook) {
			t.Fatal(err)
		}
	}
	for _, hh := range []http.Handler{r.ReportHandler(nil), r.MOHandler(nil)} {
		rec := httptest.NewRecorder()
		hh.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/bizgo/report", strings.NewReader(string(webhookBody))))
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	}
}

// Counsel-only code needs no webhook secret: the package-level parsers and the ack.
func TestCounselWithoutSecret(t *testing.T) {
	p, err := ParseCounselMessageWebhook([]byte(`{"msgKey":"K1","userKey":"USER_KEY_EXAMPLE"}`))
	if err != nil || p.MsgKey != "K1" {
		t.Fatal(p, err)
	}
	if ack := NewCounselWebhookAck(); ack.Code != "A000" || ack.Result != "Success" {
		t.Fatal(ack)
	}
	if _, err := ParseCounselMessageWebhook([]byte(`{"msgKey":1}`)); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	for _, w := range webhookTable {
		if w.signed != (w.name == "report" || w.name == "mo") {
			t.Fatalf("%s signed=%v", w.name, w.signed)
		}
	}
}

// Every webhook example of the spec parses, and the counsel payload String hides user content.
func TestWebhookExamplesParse(t *testing.T) {
	parsers := map[string]func([]byte) (any, error){
		"counselMessage":        func(b []byte) (any, error) { return ParseCounselMessageWebhook(b) },
		"counselResult":         func(b []byte) (any, error) { return ParseCounselResultWebhook(b) },
		"counselPersonalInfo":   func(b []byte) (any, error) { return ParseCounselPersonalInfoWebhook(b) },
		"counselCertResult":     func(b []byte) (any, error) { return ParseCounselCertResultWebhook(b) },
		"counselSeenInfo":       func(b []byte) (any, error) { return ParseCounselSeenInfoWebhook(b) },
		"counselReference":      func(b []byte) (any, error) { return ParseCounselReferenceWebhook(b) },
		"counselExpiredSession": func(b []byte) (any, error) { return ParseCounselExpiredSessionWebhook(b) },
	}
	n := 0
	for _, w := range loadSpec(t).Webhooks {
		parse := parsers[w.Name]
		if parse == nil || len(w.Example) == 0 {
			continue
		}
		n++
		v, err := parse(w.Example)
		if err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		mustNotContain(t, fmt.Sprintf("%v %+v", v, v), "01000000000", "CERT_RESULT_ENCRYPTED_EXAMPLE", "NICKNAME_EXAMPLE", "상담 문의 내용")
	}
	if n != len(parsers) {
		t.Fatalf("%d examples", n)
	}
}

func runtimeGOOS() string { return runtime.GOOS }

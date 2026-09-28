package bizgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Nothing in this file logs or returns request bodies, header values or query strings:
// they can contain the API key and phone numbers.

const (
	maxResponseBytes = 16 << 20 // after decompression: a broken server must not exhaust memory
	maxJSONDepth     = 64       // responses and webhooks (SDK-DESIGN.md §12.8)
	maxBackoff       = 8 * time.Second
	maxRetryAfter    = 60 * time.Second
)

// retryPolicy says which failures may be retried.
type retryPolicy int

const (
	// retrySafe: reads, DELETE (ack) and sends with an idempotency key. Retry on 429, 5xx and
	// connection errors.
	retrySafe retryPolicy = iota
	// retryRateLimitOnly: sends without an idempotency key, uploads and other creating requests,
	// which could be executed twice. Retry only on 429, which the gateway returns before
	// processing the request.
	retryRateLimitOnly
)

type transport struct {
	apiKey     secret
	baseURL    string
	timeout    time.Duration
	maxRetries int
	http       *http.Client
	logger     *slog.Logger
	sleep      func(context.Context, time.Duration) error
	userAgent  string
	limiter    *rateLimiter // nil: off
	hooks      []Hooks
}

type request struct {
	op          *operation
	method      string
	path        string // already escaped
	query       url.Values
	header      map[string]string // spec header parameters (never the SDK's own headers)
	body        []byte            // sent again as is on every attempt
	contentType string
	policy      retryPolicy
	cost        int // send bucket: number of recipients (0 or 1: one token)
}

// response is a checked JSON body.
type response struct {
	body       []byte
	status     int
	trackingID string
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func shouldRetry(policy retryPolicy, status int, connectionError bool) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if policy == retryRateLimitOnly {
		return false
	}
	if connectionError {
		return true
	}
	switch status {
	case 500, 502, 503, 504:
		return true
	}
	return false
}

// backoff is Retry-After when the server sent a valid one, otherwise exponential with jitter.
func backoff(attempt int, retryAfter time.Duration, hasRetryAfter bool) time.Duration {
	if hasRetryAfter {
		return retryAfter
	}
	base := min(time.Duration(float64(500*time.Millisecond)*float64(uint(1)<<min(attempt, 10))), maxBackoff)
	return time.Duration(float64(base) * (0.75 + rand.Float64()*0.5)) // #nosec G404 -- jitter, not crypto
}

// parseRetryAfter accepts only a finite, non-negative number of seconds (capped at 60). HTTP dates,
// negative, NaN, infinite, hexadecimal or otherwise odd values are ignored (default backoff).
func parseRetryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" || len(v) > 16 || strings.Trim(v, "0123456789.") != "" || strings.Count(v, ".") > 1 || v == "." {
		return 0, false
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 {
		return 0, false
	}
	return min(time.Duration(secs*float64(time.Second)), maxRetryAfter), true
}

// call sends the request, decodes the checked body into a response model and reports the call to
// the hooks. A body that does not decode is an *InvalidResponseError (never a success for hooks).
func call[T any](ctx context.Context, t *transport, r request) (*T, error) {
	if t == nil {
		return nil, errZeroClient
	}
	if ctx == nil {
		return nil, invalid("ctx", "context가 nil입니다. context.Background() 등을 넘기세요")
	}
	ev := t.hookStart(ctx, r)
	finished := false
	defer func() {
		// a panic (for example in a user RoundTripper) or runtime.Goexit still ends the hook call
		// (and its span); the panic then continues
		if !finished {
			p := recover()
			t.hookEnd(ev, 0, 0, &PanicError{Type: panicType(p)})
			if p != nil {
				panic(p)
			}
		}
	}()
	resp, attempts, err := t.do(ctx, r)
	var out *T
	if err == nil {
		out, err = decode[T](resp, r)
	}
	finished = true
	t.hookEnd(ev, resp.status, attempts, err)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// do sends the request with retries and returns the checked JSON body.
func (t *transport) do(ctx context.Context, r request) (response, int, error) {
	target := t.baseURL + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	for attempt := 0; ; attempt++ {
		last := attempt >= t.maxRetries
		if err := t.waitRate(ctx, r); err != nil {
			return response{}, attempt, connectionError(err, err)
		}
		started := time.Now()
		status, header, body, err := t.attempt(ctx, r, target)
		if err != nil {
			t.log(ctx, r, errorLabel(err), started, attempt)
			if ctx.Err() != nil {
				return response{}, attempt + 1, connectionError(err, ctx.Err())
			}
			var invalid *InvalidResponseError
			if errors.As(err, &invalid) {
				return response{status: invalid.HTTPStatus}, attempt + 1, err // redirects and oversized bodies are never retried
			}
			if last || !shouldRetry(r.policy, 0, true) {
				return response{}, attempt + 1, connectionError(err, nil)
			}
			if err := t.sleep(ctx, backoff(attempt, 0, false)); err != nil {
				return response{}, attempt + 1, connectionError(err, err)
			}
			continue
		}
		t.log(ctx, r, strconv.Itoa(status), started, attempt)
		if !last && shouldRetry(r.policy, status, false) {
			ra, ok := parseRetryAfter(header)
			if err := t.sleep(ctx, backoff(attempt, ra, ok)); err != nil {
				return response{status: status}, attempt + 1, connectionError(err, err)
			}
			continue
		}
		result, err := checkResponse(status, header, body)
		if err != nil {
			var apiErr *APIError
			if attempt > 0 && errors.As(err, &apiErr) && errors.Is(apiErr, ErrDuplicateRequest) {
				apiErr.AlreadyAccepted = true
				apiErr.Message = "이전 시도가 이미 접수된 것으로 보입니다(같은 idempotencyKey). 메시지는 다시 발송되지 않았으며, " +
					"접수 결과는 상태 조회 API로 확인하세요"
			}
			return response{status: status, trackingID: trackingIDOf(err)}, attempt + 1, err
		}
		return result, attempt + 1, nil
	}
}

func trackingIDOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.TrackingID
	}
	return ""
}

// waitRate takes the tokens of one attempt from the client's buckets.
func (t *transport) waitRate(ctx context.Context, r request) error {
	if t.limiter == nil || r.op == nil {
		return nil
	}
	if wait := t.limiter.reserve(r.op.rate, r.cost); wait > 0 {
		return t.sleep(ctx, wait)
	}
	return nil
}

// attempt performs one HTTP exchange. Errors never contain the URL.
func (t *transport) attempt(ctx context.Context, r request, target string) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(actx, r.method, target, body)
	if err != nil {
		return 0, nil, nil, errors.New("요청을 만들지 못했습니다")
	}
	for name, value := range r.header {
		req.Header.Set(name, value)
	}
	// set last, so that nothing else can replace them
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("X-Bizgo-Client", sdkClient)
	req.Header.Set("Authorization", string(t.apiKey))
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, nil, nil, stripURL(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// net/http decompresses gzip transparently, so this counts decompressed bytes
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, stripURL(err)
	}
	if len(data) > maxResponseBytes {
		return 0, nil, nil, &InvalidResponseError{HTTPStatus: resp.StatusCode, msg: "응답 본문이 너무 큽니다(16MB 초과)"}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return 0, nil, nil, redirectResponse(resp.StatusCode)
	}
	return resp.StatusCode, resp.Header, data, nil
}

// redirectError is returned by CheckRedirect; it carries the status of the redirect response.
type redirectError struct{ status int }

func (e *redirectError) Error() string {
	return "bizgo: redirect refused (HTTP " + strconv.Itoa(e.status) + ")"
}

func redirectResponse(status int) *InvalidResponseError {
	return &InvalidResponseError{HTTPStatus: status, msg: fmt.Sprintf("서버가 리다이렉트(HTTP %d)를 응답했습니다. "+
		"API Key를 다른 주소로 보내지 않도록 따르지 않았습니다. baseURL을 확인하세요", status)}
}

// stripURL removes *url.Error, whose message contains the full URL including the query string.
func stripURL(err error) error {
	var re *redirectError
	if errors.As(err, &re) {
		return redirectResponse(re.status)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func connectionError(err, ctxErr error) *ConnectionError {
	if ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
		return &ConnectionError{msg: "요청이 취소되었습니다", cause: ctxErr}
	}
	if isTimeout(err) {
		return &ConnectionError{Timeout: true, msg: "요청 시간이 초과되었습니다", cause: err}
	}
	return &ConnectionError{msg: fmt.Sprintf("서버에 연결하지 못했습니다(%s)", errorLabel(err)), cause: err}
}

// errorLabel is a short, value-free description of a transport error for logs and messages.
func errorLabel(err error) string {
	switch {
	case isTimeout(err):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var ne *net.OpError
	if errors.As(err, &ne) {
		return ne.Op + " error"
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return "dns error"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "connection closed"
	}
	return "network error"
}

func (t *transport) log(ctx context.Context, r request, status string, started time.Time, attempt int) {
	if t.logger == nil {
		return
	}
	defer func() { _ = recover() }() // a broken log handler must not change the result
	if !t.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	// path only: query strings can carry phone numbers (MO history filters)
	t.logger.DebugContext(ctx, fmt.Sprintf("%s %s -> %s (%d ms, attempt %d)",
		r.method, r.path, status, time.Since(started).Milliseconds(), attempt+1))
}

type envelope struct {
	Common json.RawMessage `json:"common"`
	Data   json.RawMessage `json:"data"`
}

type resultPart struct {
	code, result, trackingID string
	present                  bool
}

func parsePart(raw json.RawMessage, codeKey, resultKey string) resultPart {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return resultPart{}
	}
	return resultPart{
		code:       jsonText(m[codeKey]),
		result:     jsonText(m[resultKey]),
		trackingID: jsonText(m["infobankTrId"]),
		present:    true,
	}
}

// jsonText returns a JSON string's value, or the raw JSON of another scalar.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// checkResponse applies the documented order: non-JSON -> common.authCode -> data.code -> HTTP status.
func checkResponse(status int, header http.Header, body []byte) (response, error) {
	var env envelope
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !jsonDepthOK(trimmed, maxJSONDepth) || json.Unmarshal(trimmed, &env) != nil {
		if status >= 400 {
			e := newAPIError(status, "", LayerGateway, fmt.Sprintf("응답 본문이 JSON이 아닙니다(HTTP %d)", status), "", nil)
			e.RetryAfter, _ = parseRetryAfter(header)
			return response{}, e
		}
		return response{}, &InvalidResponseError{HTTPStatus: status, Body: body, msg: "응답 본문이 JSON 객체가 아니거나 너무 깊게 중첩되어 있습니다"}
	}
	if duplicateEnvelopeKeys(trimmed) {
		// {"common":{"authCode":"A401"},"common":{"authCode":"A000"}} must not be read as a success
		return response{}, &InvalidResponseError{HTTPStatus: status, Body: body, msg: "응답 봉투(common/data)에 같은 키가 두 번 있습니다"}
	}
	common := parsePart(env.Common, "authCode", "authResult")
	data := parsePart(env.Data, "code", "result")
	fail := func(code, message string, layer Layer) error {
		e := newAPIError(status, code, layer, message, common.trackingID, body)
		if errors.Is(e, ErrRateLimit) {
			e.RetryAfter, _ = parseRetryAfter(header)
		}
		return e
	}
	if common.code != "" && common.code != "A000" {
		return response{}, fail(common.code, common.result, LayerGateway)
	}
	if data.code != "" && data.code != "A000" {
		return response{}, fail(data.code, data.result, LayerService)
	}
	if status >= 400 {
		return response{}, fail("", "요청이 실패했습니다", LayerGateway)
	}
	return response{body: body, status: status, trackingID: common.trackingID}, nil
}

// decode parses a checked body into a response model. A mismatch is an *InvalidResponseError that
// keeps the status, the tracking ID and the body; for a send it says the request may have been accepted.
func decode[T any](resp response, r request) (*T, error) {
	var v T
	if err := json.Unmarshal(resp.body, &v); err != nil {
		msg := "응답 형식이 스펙과 다릅니다(" + jsonErrorKind(err) + ")"
		if r.op != nil && r.op.rate == bucketSend {
			msg += ". 요청은 접수됐을 수 있으니 상태 조회로 확인하세요"
		}
		return nil, &InvalidResponseError{HTTPStatus: resp.status, TrackingID: resp.trackingID, Body: resp.body, msg: msg}
	}
	return &v, nil
}

// jsonErrorKind describes a decode error without echoing any value.
func jsonErrorKind(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		if te.Field != "" {
			return "필드 " + te.Field + "의 타입이 다릅니다"
		}
		return "타입이 다릅니다"
	}
	return "JSON 구문 오류"
}

// jsonDepthOK reports whether the arrays and objects of a JSON text nest at most max levels.
// It does not validate the JSON; json.Unmarshal does.
func jsonDepthOK(data []byte, max int) bool {
	depth := 0
	inString, escaped := false, false
	for _, c := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > max {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}

// duplicateEnvelopeKeys reports whether the top-level object, or the common or data object, has the
// same key twice. encoding/json keeps the last one, which could turn a failure into a success.
func duplicateEnvelopeKeys(body []byte) bool {
	dup := false
	if walkKeys(body, func(key string, value json.RawMessage) {
		if key == "common" || key == "data" {
			if v := bytes.TrimSpace(value); len(v) > 0 && v[0] == '{' && walkKeys(v, nil) {
				dup = true
			}
		}
	}) {
		return true
	}
	return dup
}

// walkKeys calls fn for each member of a JSON object and reports whether a key occurs twice.
func walkKeys(obj []byte, fn func(key string, value json.RawMessage)) bool {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		key, _ := tok.(string)
		if seen[key] {
			return true
		}
		seen[key] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return false
		}
		if fn != nil {
			fn(key, value)
		}
	}
	return false
}

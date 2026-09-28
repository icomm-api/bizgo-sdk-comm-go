// Package bizgotest lets you test your own code that uses the Bizgo SDK without a network or an
// API key (SDK-DESIGN.md §11.3). Production code never needs to import it.
//
//	fake := bizgotest.New()
//	client, err := fake.Client() // placeholder key, sandbox URL, no rate limit, never connects
//
//	_, err = client.Send.SMS(ctx, bizgo.SMSParams{To: bizgo.To("01000000000"), From: "01000000000", Text: "hi"})
//	fake.LastRequest().OperationID // "sendOmni"
//	fake.LastRequest().JSON        // the JSON body that would have been sent
//
//	fake.On("sendOmni").Fail(bizgo.LayerService, 200, "A020") // next call: bizgo.ErrRateLimit
//	fake.On("sendOmni").SendResult("A000", "A306")            // then: the second recipient is rejected
//	fake.OnRoute("GET", "/api/comm/v1/report/inquiry/{msgKey}").Data(map[string]any{"report": []any{}})
//
//	header, body, err := bizgotest.SignWebhook([]byte("test-webhook-secret"), payload, time.Time{})
//
// Every request is recorded with its operation, method, path, query and JSON body (or multipart
// field names and file sizes). Header values, including the API key, are never recorded. Without a
// stub an operation answers with a success envelope: sends (sendOmni, createReservation,
// addReservationRecipients) accept every recipient with a fake msgKey (createReservation also
// returns a fake resvKey); MMS and RCS uploads return a fake fileKey or media; the other operations
// return data.data = {}. An unknown path answers HTTP 404 (gateway A404).
package bizgotest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
)

// FakeAPIKey is the placeholder API key used by [Fake.Client].
const FakeAPIKey = "test-api-key-not-real"

// SuccessEnvelope returns {"common": {"authCode": "A000", ...}, "data": {"code": "A000", "result": "Success", "data": data}}.
// extra members are put next to data.data (for example "resvKey").
func SuccessEnvelope(data any, extra map[string]any) map[string]any {
	inner := map[string]any{"code": "A000", "result": "Success"}
	for k, v := range extra {
		inner[k] = v
	}
	if data != nil {
		inner["data"] = data
	}
	return map[string]any{"common": map[string]any{"authCode": "A000", "authResult": "Success", "infobankTrId": "FAKE-TR-ID"}, "data": inner}
}

// ErrorEnvelope returns an error envelope: gateway errors set common.authCode, service errors data.code.
func ErrorEnvelope(layer bizgo.Layer, code string) map[string]any {
	if layer == bizgo.LayerGateway {
		return map[string]any{"common": map[string]any{"authCode": code, "authResult": "Failed", "infobankTrId": "FAKE-TR-ID"}}
	}
	return map[string]any{
		"common": map[string]any{"authCode": "A000", "authResult": "Success", "infobankTrId": "FAKE-TR-ID"},
		"data":   map[string]any{"code": code, "result": "Failed"},
	}
}

// FormValue is one multipart field as recorded: text (JSON parts included) or a file (name, type and size only).
type FormValue struct {
	Text        string
	Filename    string // set for files
	ContentType string
	Size        int
}

// Request is one recorded request.
type Request struct {
	OperationID  string // matched operationId ("" for an unknown path)
	Operation    string // <resource>.<method> of the matched operation
	Method       string
	Path         string // real (escaped) path
	PathTemplate string
	Query        url.Values
	JSON         any                    // parsed JSON body, if any
	Form         map[string][]FormValue // multipart fields, if any
	HeaderNames  []string               // names (not values) of the request headers, sorted
}

// String describes the request without its body or query.
func (r Request) String() string {
	return "bizgotest.Request(" + r.Method + " " + r.PathTemplate + ")"
}

type reply func(req *Request) (*http.Response, error)

// Fake is an in-memory stand-in for the Bizgo API: an http.RoundTripper. Plug it into a client
// with [Fake.Client], or pass &http.Client{Transport: fake} with bizgo.WithHTTPClient. It is safe
// for concurrent use.
type Fake struct {
	mu       sync.Mutex
	requests []Request
	stubs    map[string][]reply
	msgKeys  int
}

// BizgoTestTransport marks the fake as a transport bizgo.WithHTTPClient accepts without
// bizgo.WithTrustedHTTPClient (it never sends anything over the network).
func (f *Fake) BizgoTestTransport() {}

// New returns an empty fake.
func New() *Fake { return &Fake{stubs: map[string][]reply{}} }

// Client returns a bizgo.Client that talks to this fake: placeholder key, sandbox URL, rate limit
// off. opts are applied after these (for example bizgo.WithMaxRetries(0) or bizgo.WithHooks).
func (f *Fake) Client(opts ...bizgo.Option) (*bizgo.Client, error) {
	all := []bizgo.Option{
		bizgo.WithAPIKey(FakeAPIKey), bizgo.WithEnvironment(bizgo.Sandbox), bizgo.WithoutRateLimit(),
		bizgo.WithHTTPClient(&http.Client{Transport: f}),
	}
	return bizgo.NewClient(append(all, opts...)...)
}

// Stub queues responses for one operation. Each call adds one response; the last one repeats.
type Stub struct {
	f  *Fake
	id string
}

// On stubs an operation by operationId. It panics if the operation does not exist (a test bug).
func (f *Fake) On(operationID string) *Stub {
	for _, op := range bizgo.Operations() {
		if op.ID == operationID {
			return &Stub{f: f, id: op.ID}
		}
	}
	panic("bizgotest: unknown operation " + operationID)
}

// OnRoute stubs an operation by HTTP method and path template. It panics if there is none.
func (f *Fake) OnRoute(method, pathTemplate string) *Stub {
	for _, op := range bizgo.Operations() {
		if op.Method == strings.ToUpper(method) && op.PathTemplate == pathTemplate {
			return &Stub{f: f, id: op.ID}
		}
	}
	panic("bizgotest: unknown route " + method + " " + pathTemplate)
}

func (s *Stub) add(r reply) *Stub {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.stubs[s.id] = append(s.f.stubs[s.id], r)
	return s
}

// Respond answers with a status and a JSON body (any value encoding/json accepts, or []byte / string as is).
func (s *Stub) Respond(status int, body any) *Stub { return s.RespondWithHeader(status, body, nil) }

// RespondWithHeader is [Stub.Respond] with response headers (for example Retry-After).
func (s *Stub) RespondWithHeader(status int, body any, header http.Header) *Stub {
	data, err := encodeBody(body)
	return s.add(func(req *Request) (*http.Response, error) {
		if err != nil {
			return nil, err
		}
		return response(status, data, header), nil
	})
}

// Data answers with a success envelope whose data.data is data.
func (s *Stub) Data(data any) *Stub { return s.Respond(200, SuccessEnvelope(data, nil)) }

// SendResult answers a send with one destination result per code: "A000" accepted, "A301" already
// accepted with the same idempotency key (in SendResult.Duplicates), anything else rejected.
func (s *Stub) SendResult(codes ...string) *Stub {
	return s.add(func(req *Request) (*http.Response, error) {
		data, err := json.Marshal(s.f.sendBody(req.JSON, codes))
		return response(200, data, nil), err
	})
}

// Fail answers with an API error, for example Fail(bizgo.LayerService, 200, "A020") or
// Fail(bizgo.LayerGateway, 401, "A401"). Retried statuses (429, 5xx) come with "Retry-After: 0"
// so that your tests do not wait for the backoff.
func (s *Stub) Fail(layer bizgo.Layer, status int, code string) *Stub {
	var h http.Header
	if status == 429 || status >= 500 {
		h = http.Header{"Retry-After": {"0"}}
	}
	return s.RespondWithHeader(status, ErrorEnvelope(layer, code), h)
}

// NetworkError fails like a refused connection (bizgo.ErrConnection).
func (s *Stub) NetworkError() *Stub {
	return s.add(func(*Request) (*http.Response, error) { return nil, errors.New("connection refused (fake)") })
}

// Timeout fails like a timeout (bizgo.ErrTimeout).
func (s *Stub) Timeout() *Stub {
	return s.add(func(*Request) (*http.Response, error) { return nil, timeoutError{} })
}

// Default answers with the default success response.
func (s *Stub) Default() *Stub { return s.add(s.f.defaultResponse) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout (fake)" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Requests returns every recorded request, oldest first.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// RequestsFor returns the recorded requests of one operation.
func (f *Fake) RequestsFor(operationID string) []Request {
	var out []Request
	for _, r := range f.Requests() {
		if r.OperationID == operationID {
			out = append(out, r)
		}
	}
	return out
}

// LastRequest returns the most recent request (zero Request if there is none).
func (f *Fake) LastRequest() Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return Request{}
	}
	return f.requests[len(f.requests)-1]
}

// Reset forgets the recorded requests and the stubs.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests, f.stubs = nil, map[string][]reply{}
}

// String describes the fake.
func (f *Fake) String() string {
	return "bizgotest.Fake(requests=" + strconv.Itoa(len(f.Requests())) + ")"
}

// RoundTrip implements http.RoundTripper.
func (f *Fake) RoundTrip(httpReq *http.Request) (*http.Response, error) {
	var body []byte
	if httpReq.Body != nil {
		var err error
		body, err = io.ReadAll(httpReq.Body)
		_ = httpReq.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	req := record(httpReq, body)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	var r reply
	if queue := f.stubs[req.OperationID]; len(queue) > 0 {
		r = queue[0]
		if len(queue) > 1 {
			f.stubs[req.OperationID] = queue[1:]
		}
	}
	f.mu.Unlock()
	if req.OperationID == "" {
		data, _ := json.Marshal(ErrorEnvelope(bizgo.LayerGateway, "A404"))
		return withRequest(response(404, data, nil), httpReq), nil
	}
	if r == nil {
		r = f.defaultResponse
	}
	resp, err := r(&req)
	if err != nil {
		return nil, err
	}
	return withRequest(resp, httpReq), nil
}

func withRequest(resp *http.Response, req *http.Request) *http.Response {
	resp.Request = req
	return resp
}

func response(status int, body []byte, header http.Header) *http.Response {
	h := http.Header{"Content-Type": {"application/json"}}
	for k, v := range header {
		h[k] = v
	}
	return &http.Response{
		StatusCode: status, Status: strconv.Itoa(status) + " " + http.StatusText(status), Header: h,
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}
}

func encodeBody(body any) ([]byte, error) {
	switch b := body.(type) {
	case []byte:
		return b, nil
	case string:
		return []byte(b), nil
	}
	return json.Marshal(body)
}

// match finds the operation of a request; literal paths win over templates.
func match(method, path string) (bizgo.OperationInfo, bool) {
	var best bizgo.OperationInfo
	bestVars := -1
	for _, op := range bizgo.Operations() {
		if op.Method != method || !templateMatches(op.PathTemplate, path) {
			continue
		}
		vars := strings.Count(op.PathTemplate, "{")
		if bestVars < 0 || vars < bestVars {
			best, bestVars = op, vars
		}
	}
	return best, bestVars >= 0
}

func templateMatches(template, path string) bool {
	ts, ps := strings.Split(template, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if ts[i] != ps[i] {
			return false
		}
	}
	return true
}

func record(httpReq *http.Request, body []byte) Request {
	path := httpReq.URL.EscapedPath()
	req := Request{Method: httpReq.Method, Path: path, Query: httpReq.URL.Query()}
	if op, ok := match(httpReq.Method, path); ok {
		req.OperationID, req.Operation, req.PathTemplate = op.ID, op.Name, op.PathTemplate
	}
	for name := range httpReq.Header {
		req.HeaderNames = append(req.HeaderNames, name)
	}
	sort.Strings(req.HeaderNames)
	mediaType, params, _ := mime.ParseMediaType(httpReq.Header.Get("Content-Type"))
	switch {
	case mediaType == "application/json" && len(body) > 0:
		var v any
		if json.Unmarshal(body, &v) == nil {
			req.JSON = v
		}
	case mediaType == "multipart/form-data":
		req.Form = map[string][]FormValue{}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			content, _ := io.ReadAll(part)
			v := FormValue{Filename: part.FileName(), ContentType: part.Header.Get("Content-Type"), Size: len(content)}
			if v.Filename == "" {
				v.Text = string(content)
			}
			req.Form[part.FormName()] = append(req.Form[part.FormName()], v)
		}
	}
	return req
}

func (f *Fake) defaultResponse(req *Request) (*http.Response, error) {
	var body any
	switch req.OperationID {
	case "sendOmni", "createReservation", "addReservationRecipients":
		n := max(len(destinations(req.JSON)), 1)
		codes := make([]string, n)
		for i := range codes {
			codes[i] = "A000"
		}
		env := f.sendBody(req.JSON, codes)
		if req.OperationID == "createReservation" {
			env["data"].(map[string]any)["resvKey"] = "FAKE-RESVKEY-000001" // next to data.data (x-sdk-result: data)
		}
		body = env
	case "uploadMmsFile":
		body = SuccessEnvelope(map[string]any{"fileKey": "FAKE-FILEKEY-000001"}, nil)
	case "uploadRcsFile":
		body = SuccessEnvelope(map[string]any{"media": "FAKE-MEDIA-000001"}, nil)
	default:
		body = SuccessEnvelope(map[string]any{}, nil)
	}
	data, err := json.Marshal(body)
	return response(200, data, nil), err
}

func (f *Fake) sendBody(request any, codes []string) map[string]any {
	given := destinations(request)
	out := make([]any, 0, len(codes))
	for i, code := range codes {
		entry := map[string]any{}
		if i < len(given) {
			if to, ok := given[i]["to"].(string); ok {
				entry["to"] = to
			}
			if ref, ok := given[i]["ref"].(string); ok {
				entry["ref"] = ref
			}
		}
		f.mu.Lock()
		f.msgKeys++
		entry["msgKey"] = fmt.Sprintf("FAKE-MSGKEY-%06d", f.msgKeys)
		f.mu.Unlock()
		entry["code"] = code
		entry["result"] = "Success"
		if code != "A000" {
			entry["result"] = "Failed"
		}
		out = append(out, entry)
	}
	var extra map[string]any
	if m, ok := request.(map[string]any); ok {
		if ref, ok := m["ref"].(string); ok {
			extra = map[string]any{"ref": ref}
		}
	}
	return SuccessEnvelope(map[string]any{"destinations": out}, extra)
}

func destinations(request any) []map[string]any {
	m, ok := request.(map[string]any)
	if !ok {
		return nil
	}
	items, _ := m["destinations"].([]any)
	var out []map[string]any
	for _, it := range items {
		if d, ok := it.(map[string]any); ok {
			out = append(out, d)
		}
	}
	return out
}

// SignWebhook builds the headers and body of a signed webhook request, for testing your
// [bizgo.WebhookReceiver] handlers: X-IB-Timestamp (timestamp in epoch milliseconds, as Bizgo sends
// it; the zero time means now), X-IB-Signature = hex(HMAC-SHA256(secret, timestamp)) and the JSON
// body (payload as is when it is []byte or string, otherwise encoded).
//
// Only report and MO webhooks are signed. Counsel talk (상담톡) webhooks have no signature: test
// those handlers with a plain JSON POST (httptest.NewRequest) without these headers.
func SignWebhook(secret []byte, payload any, timestamp time.Time) (http.Header, []byte, error) {
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	stamp := strconv.FormatInt(timestamp.UnixMilli(), 10)
	body, err := encodeBody(payload)
	if err != nil {
		return nil, nil, err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(bizgo.WebhookTimestampHeader, stamp)
	h.Set(bizgo.WebhookSignatureHeader, Signature(secret, stamp))
	return h, body, nil
}

// Signature returns hex(HMAC-SHA256(secret, timestamp)), the X-IB-Signature of a timestamp.
func Signature(secret []byte, timestamp string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureBase64 is [Signature] in base64 (Bizgo does not document the encoding; the SDK accepts both).
func SignatureBase64(secret []byte, timestamp string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// NewWebhookRequest returns a signed POST request for an http.Handler test (httptest.NewRecorder).
func NewWebhookRequest(ctx context.Context, target string, secret []byte, payload any, timestamp time.Time) (*http.Request, error) {
	h, body, err := SignWebhook(secret, payload, timestamp)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = h
	return req, nil
}

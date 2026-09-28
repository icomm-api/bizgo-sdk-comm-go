package bizgo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

const (
	statsRoute = "/api/comm/v1/message/statistics"
	moRoute    = "/api/comm/v1/message/history/mo"
)

var day = Date(2026, 1, 1)

func stats(c *Client) error {
	_, err := c.Messages.Statistics(context.Background(), StatisticsParams{StartDate: day})
	return err
}

func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("not an APIError: %#v", err)
	}
	return apiErr
}

func TestGatewayAuthFailureWithoutData(t *testing.T) {
	// observed on sandbox: invalid key -> HTTP 401, common.authCode=A401, no data
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.JSON(401, map[string]any{
		"common": map[string]any{"authCode": "A401", "authResult": "Unauthorized", "infobankTrId": "TR-1"}}))
	err := stats(c)
	e := asAPIError(t, err)
	if !errors.Is(err, ErrAuthentication) || e.HTTPStatus != 401 || e.Code != "A401" || e.Layer != LayerGateway ||
		e.TrackingID != "TR-1" || e.Description != "" {
		t.Fatalf("err = %#v", e)
	}
	if err.Error() != "HTTP 401 | gateway code=A401 | Unauthorized | infobankTrId=TR-1" {
		t.Fatalf("message = %q", err.Error())
	}
	mustNotContain(t, fmt.Sprintf("%v %+v %#v %s", err, err, err, err), apiKey)
}

func TestServiceErrorCodeOnHTTP200UsesDocumentedStatus(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.JSON(200, ts.Envelope(nil, "A306", "")))
	err := stats(c)
	e := asAPIError(t, err)
	if !errors.Is(err, ErrBadRequest) || e.Layer != LayerService || e.Description != "유효하지 않거나 비어있는 필드 (필드명 : to)" {
		t.Fatalf("err = %#v", e)
	}
	if want := "HTTP 200 | service code=A306 | Failed | 유효하지 않거나 비어있는 필드 (필드명 : to) | infobankTrId=TR-TEST"; err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestSameCodeMeansDifferentThingsPerLayer(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", statsRoute,
		ts.JSON(400, ts.Envelope(nil, "A401", "")), // service A401 = invalid paymentCode
		ts.JSON(401, map[string]any{"common": map[string]any{"authCode": "A401", "authResult": "Unauthorized"}}))
	if err := stats(c); !errors.Is(err, ErrBadRequest) || errors.Is(err, ErrAuthentication) {
		t.Fatalf("service A401: %v", err)
	}
	if err := stats(c); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("gateway A401: %v", err)
	}
	if route.Count() != 2 {
		t.Fatalf("calls = %d", route.Count())
	}
}

func TestServiceCodesMapToTheirKinds(t *testing.T) {
	for code, kind := range map[string]error{
		"A110": ErrPermissionDenied, "A100": ErrAuthentication, "A020": ErrRateLimit, "A301": ErrDuplicateRequest,
		"A910": ErrInternalServer, "Z999": ErrAPI, // unknown code inside HTTP 200
	} {
		c, srv, _ := newTestClient(t)
		srv.On("GET", statsRoute, ts.JSON(200, ts.Envelope(nil, code, "")))
		if err := stats(c); !errors.Is(err, kind) {
			t.Fatalf("%s: %v", code, err)
		}
	}
}

func TestNotFoundStatus(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.JSON(404, map[string]any{"common": map[string]any{"authCode": "A404", "authResult": "Not Found"}}))
	if err := stats(c); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestServerErrorWithHTMLBodyIsRetriedThenRaised(t *testing.T) {
	c, srv, d := newTestClient(t)
	route := srv.On("GET", statsRoute, ts.Text(502, "<html>bad gateway</html>"))
	err := stats(c)
	e := asAPIError(t, err)
	if !errors.Is(err, ErrInternalServer) || e.HTTPStatus != 502 || e.Body != nil || route.Count() != 3 || len(d.all()) != 2 {
		t.Fatalf("err = %#v, calls = %d", e, route.Count())
	}
	mustContain(t, err.Error(), "JSON이 아닙니다")
}

func TestErrorWithEmptyBodyDoesNotBreakParsing(t *testing.T) {
	c, srv, _ := newTestClient(t, WithMaxRetries(0))
	srv.On("GET", statsRoute, ts.Text(500, ""))
	if err := stats(c); !errors.Is(err, ErrInternalServer) {
		t.Fatal(err)
	}
}

func TestSuccessStatusWithNonJSONBody(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.Text(200, "ok"))
	err := stats(c)
	var ie *InvalidResponseError
	if !errors.As(err, &ie) || ie.HTTPStatus != 200 || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestResponseWithWrongShapeIsInvalidResponse(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.OK(map[string]any{"statistics": "not-a-list"}))
	if err := stats(c); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
}

func TestResponseSizeIsCapped(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.Text(200, `{"x":"`+strings.Repeat("a", maxResponseBytes)+`"}`))
	err := stats(c)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
	mustContain(t, err.Error(), "너무 큽니다")
}

func TestConnectionErrorHidesRequestDetails(t *testing.T) {
	c, srv, d := newTestClient(t)
	route := srv.On("GET", moRoute, ts.Reply{Disconnect: true})
	_, err := c.Messages.MOHistory(context.Background(), MOHistoryParams{OccurredTime: time.Now(), From: otherPhone})
	var ce *ConnectionError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConnection) || ce.Timeout {
		t.Fatalf("err = %#v", err)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		t.Fatal("the *url.Error (with the full URL) is chained")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		mustNotContain(t, e.Error(), otherPhone, "history", "127.0.0.1:"+strings.Split(srv.URL, ":")[2]+"/")
	}
	if route.Count() != 3 || len(d.all()) != 2 { // reads are retried after connection errors
		t.Fatalf("calls = %d, delays = %v", route.Count(), d.all())
	}
}

func TestCanceledContextStopsRetries(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", statsRoute, ts.Text(503, "busy"))
	ctx, cancel := context.WithCancel(context.Background())
	c.t.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	_, err := c.Messages.Statistics(ctx, StatisticsParams{StartDate: day})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrConnection) || route.Count() != 1 {
		t.Fatalf("err = %v, calls = %d", err, route.Count())
	}
}

func TestDebugLogHasNoKeyQueryOrBody(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, srv, _ := newTestClient(t, WithLogger(logger))
	srv.On("GET", moRoute, ts.OK(map[string]any{"messages": []any{}, "hasNext": false}))
	srv.On("POST", sendRoute, accepted("A000"))
	if _, err := c.Messages.MOHistory(context.Background(), MOHistoryParams{OccurredTime: time.Now(), From: otherPhone}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send.SMS(context.Background(), SMSParams{To: To(otherPhone), From: phone, Text: "비밀 본문"}); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	mustContain(t, text, "GET /api/comm/v1/message/history/mo -> 200 (")
	mustContain(t, text, "attempt 1)")
	mustContain(t, text, "POST /api/comm/v1/send/omni -> 200")
	mustNotContain(t, text, apiKey, otherPhone, "occurredTime", "비밀")
}

func TestNoLoggerMeansSilent(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	c, srv, _ := newTestClient(t)
	srv.On("GET", statsRoute, ts.OK(nil))
	if err := stats(c); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged: %s", buf.String())
	}
}

func TestStringHidesKeyAndPersonalData(t *testing.T) {
	c, err := NewClient(WithAPIKey(apiKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", *c)} {
		mustNotContain(t, s, apiKey)
	}
	e := newAPIError(400, "A306", LayerService, "Failed", "TR", []byte(`{"to":"`+otherPhone+`"}`))
	mustNotContain(t, fmt.Sprintf("%v %+v %#v %s", e, e, e, e), otherPhone)
	if !bytes.Contains(e.Body, []byte(otherPhone)) {
		t.Fatal("Body should keep the raw response")
	}
}

func TestPrefixedBlankOrInvalidKeysAreRejected(t *testing.T) {
	for _, key := range []string{"Bearer abc", "ApiKey abc", "  ", "", "ab\tc", "키"} {
		_, err := NewClient(WithAPIKey(key))
		var ce *ConfigurationError
		if !errors.As(err, &ce) || !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%q: %v", key, err)
		}
		mustNotContain(t, err.Error(), "abc")
	}
}

func TestAPIKeyFromEnvironment(t *testing.T) {
	t.Setenv(APIKeyEnv, apiKey)
	c, err := NewClient()
	if err != nil || c.BaseURL() != string(Production) {
		t.Fatalf("%v %v", c, err)
	}
	t.Setenv(APIKeyEnv, "")
	if _, err := NewClient(); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	c, err = NewClient(WithAPIKey(apiKey), WithEnvironment(Sandbox))
	if err != nil || c.BaseURL() != "https://sandbox-mars.ibapi.kr" {
		t.Fatalf("%v %v", c, err)
	}
}

func TestInsecureOrMalformedBaseURLsAreRejected(t *testing.T) {
	for _, u := range []string{"http://mars.ibapi.kr", "ftp://example.com", "https://", "mars.ibapi.kr",
		"https://user:pw@mars.ibapi.kr", "https://mars.ibapi.kr?x=1", "http://localhost.example.com"} {
		if _, err := NewClient(WithAPIKey(apiKey), WithBaseURL(u)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if _, err := NewClient(WithAPIKey(apiKey), WithEnvironment("https://example.com")); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}

func TestLocalhostHTTPIsAllowedForMockServers(t *testing.T) {
	for _, u := range []string{"http://localhost:4010", "http://127.0.0.1:4010/", "http://[::1]:4010"} {
		c, err := NewClient(WithAPIKey(apiKey), WithBaseURL(u))
		if err != nil || c.BaseURL() != strings.TrimSuffix(u, "/") {
			t.Fatalf("%s: %v", u, err)
		}
	}
}

func TestInvalidSettingsAreRejected(t *testing.T) {
	for _, opt := range []Option{WithTimeout(0), WithMaxRetries(-1), WithHTTPClient(nil)} {
		if _, err := NewClient(WithAPIKey(apiKey), opt); !errors.Is(err, ErrConfiguration) {
			t.Fatal(err)
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	other := ts.New(t)
	stolen := other.On("GET", statsRoute, ts.OK(nil))
	for _, hc := range []*http.Client{nil, {Timeout: 5 * time.Second}} {
		opts := []Option{WithMaxRetries(0)}
		if hc != nil {
			opts = append(opts, WithHTTPClient(hc))
		}
		c, srv, _ := newTestClient(t, opts...)
		redirect := ts.Text(302, "")
		redirect.Header = http.Header{"Location": {other.URL + statsRoute}}
		srv.On("GET", statsRoute, redirect)
		err := stats(c)
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("err = %v", err)
		}
		mustContain(t, err.Error(), "리다이렉트")
		if hc != nil && hc.CheckRedirect != nil {
			t.Fatal("the caller's http.Client was modified")
		}
	}
	if stolen.Count() != 0 {
		t.Fatal("the request (with the API key) followed the redirect")
	}
}

func TestErrorKindsAreDistinct(t *testing.T) {
	e := newAPIError(429, "", LayerGateway, "", "", nil)
	if !errors.Is(e, ErrRateLimit) || errors.Is(e, ErrBadRequest) || !errors.Is(e.Kind(), ErrRateLimit) {
		t.Fatal(e)
	}
	ce := &ConnectionError{Timeout: true, msg: "t"}
	if !errors.Is(ce, ErrTimeout) || !errors.Is(ce, ErrConnection) {
		t.Fatal(ce)
	}
	if errors.Is(&ConnectionError{msg: "x"}, ErrTimeout) {
		t.Fatal("not a timeout")
	}
}

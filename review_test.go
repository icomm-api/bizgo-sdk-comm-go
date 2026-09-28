package bizgo_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

// Regression tests of the review follow-up (G1-G9) and SDK-DESIGN.md §12.11 / §12.19.

const (
	secretA = "01000001111"
	secretB = "01000002222"
	text    = "secret text 123456"
)

// G1: every parameter struct with phone numbers or content prints them masked (§12.11).
func TestG1_ParamsStructsAreMasked(t *testing.T) {
	values := map[string]any{
		"SMSParams":               bizgo.SMSParams{To: bizgo.To(secretA), From: secretB, Text: text},
		"LMSParams":               bizgo.LMSParams{To: bizgo.To(secretA), From: secretB, Text: text, Title: text},
		"MMSParams":               bizgo.MMSParams{To: bizgo.To(secretA), From: secretB, Text: text, FileKeys: []string{"F"}},
		"OmniParams":              bizgo.OmniParams{To: bizgo.To(secretA), Messages: []bizgo.ChannelMessage{&bizgo.SMSMessage{From: secretB, Text: text}}},
		"BulkParams":              bizgo.BulkParams{To: bizgo.To(secretA, secretB), Messages: []bizgo.ChannelMessage{&bizgo.SMSMessage{From: secretB, Text: text}}},
		"MOHistoryParams":         bizgo.MOHistoryParams{OccurredTime: time.Now(), From: secretA, To: secretB},
		"UploadParams":            bizgo.UploadParams{File: strings.NewReader(text), Filename: "a.jpg"},
		"UploadFile":              bizgo.UploadFile{Reader: strings.NewReader(text), Filename: "a.jpg"},
		"CreateKakaoSenderParams": bizgo.CreateKakaoSenderParams{PhoneNumber: secretA, Token: "123456"},
		"SendOmniRequest":         &bizgo.SendOmniRequest{Destinations: bizgo.To(secretA), MessageFlow: []bizgo.MessageFlowItem{{SMS: &bizgo.SMSMessage{From: secretB, Text: text}}}},
	}
	for name, v := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(format, v)
			for _, secret := range []string{secretA, secretB, text, "123456"} {
				if strings.Contains(out, secret) {
					t.Errorf("%s %s: %s", name, format, out)
				}
			}
		}
		// the pointer prints the same way
		if p := reflect.New(reflect.TypeOf(v)); p.Elem().Kind() == reflect.Struct {
			p.Elem().Set(reflect.ValueOf(v))
			if out := fmt.Sprintf("%+v", p.Interface()); strings.Contains(out, secretA) {
				t.Errorf("*%s: %s", name, out)
			}
		}
	}
	if out := fmt.Sprint(values["CreateKakaoSenderParams"]); !strings.Contains(out, "010****1111") || !strings.Contains(out, "token:[REDACTED]") {
		t.Fatal(out)
	}
}

// G2 / §12.19: a success response without data.data (or without data) never gives a nil result.
func TestG2_SuccessWithoutDataIsNeverNil(t *testing.T) {
	envelopes := []string{
		`{"common":{"authCode":"A000","authResult":"Success"},"data":{"code":"A000","result":"Success"}}`,
		`{"common":{"authCode":"A000","authResult":"Success"},"data":{"code":"A000","result":"Success","data":null}}`,
		`{"common":{"authCode":"A000","authResult":"Success"}}`,
	}
	checked := 0
	for _, op := range bizgo.Operations() {
		for _, key := range []string{op.ID, op.ID + "+files"} {
			call, ok := generatedCalls[key]
			if !ok || generatedVoid[key] {
				continue
			}
			for _, env := range envelopes {
				fake := bizgotest.New()
				fake.On(op.ID).Respond(200, env)
				result, err := call(context.Background(), mustClient(t, fake))
				if err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				if v := reflect.ValueOf(result); !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Slice || v.Kind() == reflect.Map) && v.IsNil() {
					t.Fatalf("%s: nil result for %s", key, env)
				}
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("only %d methods checked", checked)
	}
	// hand-written methods
	fake := bizgotest.New()
	for _, op := range []string{"getReportInquiry", "getMessageStatusByMsgKey", "getMessageStatistics", "getMessageHistory", "getMoByMsgKey", "getReportPolling"} {
		fake.On(op).Respond(200, envelopes[0])
	}
	c := mustClient(t, fake)
	ctx := context.Background()
	reports, err1 := c.Reports.Inquiry(ctx, "K")
	statuses, err2 := c.Messages.Status(ctx, "K")
	stats, err3 := c.Messages.Statistics(ctx, bizgo.StatisticsParams{StartDate: time.Now()})
	page, err4 := c.Messages.History(ctx, bizgo.HistoryParams{RequestTime: time.Now()})
	mo, err5 := c.Messages.MO(ctx, "K")
	batch, err6 := c.Reports.Poll(ctx)
	if err := errors.Join(err1, err2, err3, err4, err5, err6); err != nil {
		t.Fatal(err)
	}
	if reports == nil || statuses == nil || stats == nil || page.Messages == nil || mo == nil || batch.Reports == nil {
		t.Fatal("nil slice in a success result")
	}
}

// G4: errors keep their kind through encoding/gob.
func TestG4_ErrorsRoundTripThroughGob(t *testing.T) {
	fake := bizgotest.New()
	fake.On("sendOmni").Fail(bizgo.LayerService, 200, "A020")
	_, err := mustClient(t, fake, bizgo.WithMaxRetries(0)).Send.SMS(context.Background(), bizgo.SMSParams{To: bizgo.To(secretA), From: secretB, Text: "x"})
	var apiErr *bizgo.APIError
	if !errors.As(err, &apiErr) {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(apiErr); err != nil {
		t.Fatal(err)
	}
	var back bizgo.APIError
	if err := gob.NewDecoder(&buf).Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(&back, bizgo.ErrRateLimit) || back.Code != "A020" || back.Layer != bizgo.LayerService || back.Error() != apiErr.Error() {
		t.Fatalf("%#v", &back)
	}
	for _, e := range []error{&bizgo.InvalidResponseError{HTTPStatus: 200}, &bizgo.ConnectionError{Timeout: true}} {
		var b bytes.Buffer
		if err := gob.NewEncoder(&b).Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

// G5: a zero Client and a nil context fail with typed errors instead of panicking.
func TestG5_ZeroClientAndNilContext(t *testing.T) {
	var zero bizgo.Client
	ctx := context.Background()
	if s := zero.String(); !strings.Contains(s, "NewClient") || fmt.Sprintf("%v %+v %#v", zero, &zero, zero) == "" {
		t.Fatal(s)
	}
	var nilClient *bizgo.Client
	_ = nilClient.String()
	checks := map[string]error{}
	_, checks["Send.SMS"] = zero.Send.SMS(ctx, bizgo.SMSParams{To: bizgo.To(secretA), From: secretB, Text: "x"})
	_, checks["Send.Bulk"] = zero.Send.Bulk(ctx, bizgo.BulkParams{To: bizgo.To(secretA), Messages: []bizgo.ChannelMessage{&bizgo.SMSMessage{From: secretB, Text: "x"}}})
	_, checks["Reports.Poll"] = zero.Reports.Poll(ctx)
	_, checks["Files.UploadMMS"] = zero.Files.UploadMMS(ctx, bizgo.UploadParams{File: strings.NewReader("x")})
	_, checks["Alimtalk.Templates.List"] = zero.Alimtalk.Templates.List(ctx, bizgo.ListAlimtalkTemplatesParams{SenderKey: "S"})
	_, checks["Reservations.Recipients.List"] = zero.Reservations.Recipients.List(ctx, "R", bizgo.ListReservationRecipientsParams{})
	for _, err := range zero.Messages.IterHistory(ctx, bizgo.HistoryParams{RequestTime: time.Now()}) {
		checks["Messages.IterHistory"] = err
	}
	for name, err := range checks {
		if !errors.Is(err, bizgo.ErrConfiguration) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c := mustClient(t, bizgotest.New())
	//nolint:staticcheck // SA1012: a nil context on purpose
	if _, err := c.Send.SMS(nil, bizgo.SMSParams{To: bizgo.To(secretA), From: secretB, Text: "x"}); !errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
	//nolint:staticcheck // SA1012
	if _, err := c.Alimtalk.Templates.List(nil, bizgo.ListAlimtalkTemplatesParams{SenderKey: "S"}); !errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
}

type secretValue struct{ phone string }

type panickingRT struct {
	n    atomic.Int32
	next http.RoundTripper
	do   func(n int32)
}

func (p *panickingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	p.do(p.n.Add(1))
	return p.next.RoundTrip(r)
}

// G6: a chunk panic keeps only the panic's type; runtime.Goexit in a chunk is a failed chunk; hooks
// end even when a call panics.
func TestG6_PanicsAndGoexit(t *testing.T) {
	fake := bizgotest.New()
	rt := &panickingRT{next: fake, do: func(n int32) {
		switch n {
		case 2:
			panic(secretValue{phone: secretA})
		case 3:
			runtime.Goexit()
		}
	}}
	var ends atomic.Int32
	hooks := bizgo.Hooks{OnRequestEnd: func(_ context.Context, e bizgo.RequestEvent) {
		if !e.Success && e.ErrorKind == "panic" {
			ends.Add(1)
		}
	}}
	c, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithoutRateLimit(), bizgo.WithHooks(hooks),
		bizgo.WithTrustedHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Send.Bulk(context.Background(), bizgo.BulkParams{To: bizgo.To("01000000000", "01000000001", "01000000002", "01000000003"),
		Messages: []bizgo.ChannelMessage{&bizgo.SMSMessage{From: "01000000000", Text: "x"}}, ChunkSize: 1, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 2 || len(res.MsgKeys()) != 2 {
		t.Fatalf("errors %v, msgKeys %v", res.Errors, res.MsgKeys())
	}
	var pe *bizgo.PanicError
	if !errors.As(res.Errors[0].Err, &pe) || !errors.Is(res.Errors[0], bizgo.ErrChunkPanic) || pe.Type != "bizgo_test.secretValue" || pe.Goexit {
		t.Fatalf("%#v", res.Errors[0].Err)
	}
	if !errors.As(res.Errors[1].Err, &pe) || !pe.Goexit || !errors.Is(res.Errors[1], bizgo.ErrChunkPanic) {
		t.Fatalf("%#v", res.Errors[1].Err)
	}
	if out := fmt.Sprintf("%v %+v", res.Errors, res); strings.Contains(out, secretA) {
		t.Fatal(out)
	}
	if ends.Load() != 2 {
		t.Fatalf("hook ended %d panicked calls", ends.Load())
	}
	// outside a bulk send the panic continues after the hook ended
	rt.n.Store(1)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic swallowed")
			}
		}()
		_, _ = c.Reports.Poll(context.Background())
	}()
	if ends.Load() != 3 {
		t.Fatal("hook not ended after a panic")
	}
}

// G7: short numbers are masked by at least half (§12.11).
func TestG7_MaskShortNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"01000001234": "010****1234", "0100000123456": "010******3456", "15880000": "158****0",
		"021234567": "021*****7", "0212345678": "021*****78", "1234567": "*******", "1234": "****", "": "",
	} {
		got := bizgo.MaskPhone(in)
		if got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
		if n := strings.Count(got, "*"); in != "" && len(in) < 11 && n*2 < len(in) {
			t.Errorf("%q: only %d of %d masked", in, n, len(in))
		}
	}
}

// G8: the README webhook example runs the handler in an http.Server with read timeouts.
func TestG8_ReadmeWebhookServerHasTimeouts(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ReadHeaderTimeout:", "ReadTimeout:", "WriteTimeout:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("README webhook example without %s", want)
		}
	}
}

// G9: an envelope with a duplicate key in common, data or at the top level is rejected.
func TestG9_DuplicateEnvelopeKeys(t *testing.T) {
	for _, body := range []string{
		`{"common":{"authCode":"A401"},"common":{"authCode":"A000"},"data":{"code":"A000"}}`,
		`{"common":{"authCode":"A401","authCode":"A000"},"data":{"code":"A000"}}`,
		`{"common":{"authCode":"A000"},"data":{"code":"A306","code":"A000"}}`,
		`{"common":{"authCode":"A000"},"data":{"code":"A000","data":{}},"data":{"code":"A000","data":{}}}`,
	} {
		fake := bizgotest.New()
		fake.On("getReportPolling").Respond(200, body)
		_, err := mustClient(t, fake).Reports.Poll(context.Background())
		if !errors.Is(err, bizgo.ErrInvalidResponse) || !strings.Contains(err.Error(), "같은 키") {
			t.Fatalf("%s: %v", body, err)
		}
	}
	// duplicates deeper in the payload are left to the models (last one wins, as encoding/json)
	fake := bizgotest.New()
	fake.On("getReportPolling").Respond(200, `{"common":{"authCode":"A000"},"data":{"code":"A000","data":{"reportId":"a","reportId":"b"}}}`)
	if b, err := mustClient(t, fake).Reports.Poll(context.Background()); err != nil || b.ReportID != "b" {
		t.Fatal(b, err)
	}
}

// G3: the otel/v* tag is verified against the published SDK (replace dropped), after the SDK tag.
func TestG3_ReleaseWorkflowVerifiesOtelAgainstPublishedSDK(t *testing.T) {
	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"otel/v*"`, "-dropreplace=github.com/icomm-api/bizgo-sdk-comm-go", "go mod tidy",
		`test "$required" = "$version"`, "go test -race", "SDK를 먼저 태그"} {
		if !strings.Contains(s, want) {
			t.Fatalf("release.yml has no %q", want)
		}
	}
}

type opaqueRT struct{}

func (opaqueRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }

// §12.6 general principle: only clients the SDK can check are accepted without an explicit opt-in;
// proxy and CA certificates are SDK options; certificate verification can never be turned off.
func TestHTTPClientPolicy(t *testing.T) {
	ok := func(opts ...bizgo.Option) error {
		_, err := bizgo.NewClient(append([]bizgo.Option{bizgo.WithAPIKey(bizgotest.FakeAPIKey)}, opts...)...)
		return err
	}
	insecure := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // G402: the case under test
	for name, err := range map[string]error{
		"opaque RoundTripper":        ok(bizgo.WithHTTPClient(&http.Client{Transport: opaqueRT{}})),
		"InsecureSkipVerify":         ok(bizgo.WithHTTPClient(&http.Client{Transport: insecure})),
		"proxy with own client":      ok(bizgo.WithHTTPClient(&http.Client{}), bizgo.WithProxy("http://proxy.example.invalid:3128")),
		"bad proxy scheme":           ok(bizgo.WithProxy("ftp://proxy.example.invalid")),
		"nil pool":                   ok(bizgo.WithRootCAs(nil)),
		"missing CA file":            ok(bizgo.WithRootCAsFile(t.TempDir() + "/secret-dir/ca.pem")),
		"CA file without a PEM cert": ok(bizgo.WithRootCAsFile(writeFile(t, "not a certificate"))),
	} {
		if !errors.Is(err, bizgo.ErrConfiguration) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-dir") {
			t.Errorf("%s: full path in %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"stock transport":   ok(bizgo.WithHTTPClient(&http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: time.Minute})),
		"default transport": ok(bizgo.WithHTTPClient(&http.Client{})),
		"bizgotest fake":    ok(bizgo.WithHTTPClient(&http.Client{Transport: bizgotest.New()})),
		"trusted opaque":    ok(bizgo.WithTrustedHTTPClient(&http.Client{Transport: opaqueRT{}})),
		"proxy":             ok(bizgo.WithProxy("socks5://proxy.example.invalid:1080")),
		"root CAs":          ok(bizgo.WithRootCAs(x509.NewCertPool())),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// §12.11: person names, secrets and replace words; slog (also JSON) logs the masked form.
func TestMaskingOfPersonsSecretsAndSlog(t *testing.T) {
	p := bizgo.CounselPersonalInfoWebhookPayload{}
	if err := json.Unmarshal([]byte(`{"msgKey":"K","personalInfo":{"nickname":"NICKNAME_EXAMPLE","phone_number":"01000001111"}}`), &p); err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("%+v", p)
	if strings.Contains(out, "NICKNAME_EXAMPLE") || strings.Contains(out, secretA) || !strings.Contains(out, "nickname:N***************") {
		t.Fatal(out)
	}
	d := bizgo.Destination{To: secretA, ReplaceWords: map[string]string{"name": "PERSON_NAME_EXAMPLE"}}
	if out := fmt.Sprintf("%#v", d); strings.Contains(out, "PERSON_NAME_EXAMPLE") || !strings.Contains(out, "replaceWords:(1 fields)") {
		t.Fatal(out)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("x", "params", bizgo.SMSParams{To: bizgo.To(secretA), From: secretB, Text: text}, "dest", d,
		"result", &bizgo.SendResult{Destinations: []bizgo.SendDestinationResult{{To: secretA}}})
	if s := buf.String(); strings.Contains(s, secretA) || strings.Contains(s, secretB) || strings.Contains(s, text) || !strings.Contains(s, "010****1111") {
		t.Fatal(s)
	}
}

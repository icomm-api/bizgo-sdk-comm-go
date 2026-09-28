package bizgootel_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
	bizgootel "github.com/icomm-api/bizgo-sdk-comm-go/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	fake := bizgotest.New()
	client, err := fake.Client(bizgo.WithHooks(bizgootel.Hooks(tp)))
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, parent := tp.Tracer("test").Start(context.Background(), "parent")
	fake.On("getReportInquiry").Fail(bizgo.LayerGateway, 503, "").Default()
	if _, err := client.Reports.Inquiry(parentCtx, "MSGKEY-SECRET"); err != nil {
		t.Fatal(err)
	}
	fake.On("sendOmni").Fail(bizgo.LayerService, 200, "A020")
	_, _ = client.Send.SMS(parentCtx, bizgo.SMSParams{To: bizgo.To("01000000000"), From: "01000000000", Text: "secret text"})
	parent.End()

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("%d spans", len(spans))
	}
	inquiry, send := spans[0], spans[1]
	attrs := func(s tracetest.SpanStub) map[string]string {
		out := map[string]string{}
		for _, a := range s.Attributes {
			out[string(a.Key)] = a.Value.String()
		}
		return out
	}
	a := attrs(inquiry)
	if inquiry.Name != "bizgo reports.inquiry" || inquiry.SpanKind != trace.SpanKindClient || inquiry.Status.Code == codes.Error ||
		a["url.template"] != "/api/comm/v1/report/inquiry/{msgKey}" || a["http.request.method"] != "GET" ||
		a["http.response.status_code"] != "200" || a["bizgo.retry_count"] != "1" || a["bizgo.operation_id"] != "getReportInquiry" {
		t.Fatalf("%s %v %v", inquiry.Name, a, inquiry.Status)
	}
	if inquiry.Parent.SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("not a child of the caller's span")
	}
	b := attrs(send)
	if send.Name != "bizgo send.omni" || send.Status.Code != codes.Error || b["bizgo.code"] != "A020" || b["bizgo.layer"] != "service" ||
		b["error.type"] != "rate limit" {
		t.Fatalf("%s %v %v", send.Name, b, send.Status)
	}
	dump := fmt.Sprintf("%+v", spans)
	for _, secret := range []string{"MSGKEY-SECRET", "01000000000", "secret text", bizgotest.FakeAPIKey} {
		if strings.Contains(dump, secret) {
			t.Fatalf("span contains %q", secret)
		}
	}
}

func TestNilProviderIsANoOp(t *testing.T) {
	fake := bizgotest.New()
	client, err := fake.Client(bizgo.WithHooks(bizgootel.Hooks(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Reports.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("transport failure") }

// The span of a call that panics is ended (with an error status) before the panic continues.
func TestSpanEndsWhenTheCallPanics(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	client, err := bizgo.NewClient(bizgo.WithAPIKey(bizgotest.FakeAPIKey), bizgo.WithoutRateLimit(),
		bizgo.WithHooks(bizgootel.Hooks(tp)), bizgo.WithTrustedHTTPClient(&http.Client{Transport: panicTransport{}}))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = client.Reports.Poll(context.Background())
	}()
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Error || spans[0].EndTime.IsZero() {
		t.Fatalf("%+v", spans)
	}
}

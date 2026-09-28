// Package bizgootel records one OpenTelemetry span per Bizgo API call (SDK-DESIGN.md §11.4).
//
// It is a separate module, so that the SDK itself keeps no dependencies:
//
//	go get github.com/icomm-api/bizgo-sdk-comm-go/otel
//
//	client, err := bizgo.NewClient(bizgo.WithHooks(bizgootel.Hooks(otel.GetTracerProvider())))
//
// The span is named "bizgo <resource>.<method>" (for example "bizgo send.omni"), has kind client and
// the attributes http.request.method, url.template (the path template, never real values),
// http.response.status_code, bizgo.operation_id, bizgo.retry_count and, for API errors, bizgo.code
// and bizgo.layer. A failed call sets the span status to Error with the error kind ("rate limit",
// "timeout", ...). Request and response bodies, query strings, header values, phone numbers and the
// API key are never recorded: the hooks do not receive them.
//
// The span covers the whole call including retries. It is a child of the span in the context you
// pass to the SDK method.
package bizgootel

import (
	"context"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of the tracer.
const ScopeName = "github.com/icomm-api/bizgo-sdk-comm-go/otel"

// Hooks returns hooks that trace every API call with a tracer of tp (nil: no tracing).
func Hooks(tp trace.TracerProvider) bizgo.Hooks {
	if tp == nil {
		return bizgo.Hooks{}
	}
	tracer := tp.Tracer(ScopeName, trace.WithInstrumentationVersion(bizgo.Version))
	return bizgo.Hooks{
		OnRequestStart: func(ctx context.Context, e bizgo.RequestEvent) context.Context {
			ctx, _ = tracer.Start(ctx, "bizgo "+e.Operation,
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithTimestamp(e.Started),
				trace.WithAttributes(
					attribute.String("http.request.method", e.Method),
					attribute.String("url.template", e.PathTemplate),
					attribute.String("bizgo.operation_id", e.OperationID),
				))
			return ctx
		},
		OnRequestEnd: func(ctx context.Context, e bizgo.RequestEvent) {
			span := trace.SpanFromContext(ctx)
			if !span.IsRecording() {
				return
			}
			attrs := []attribute.KeyValue{attribute.Int("bizgo.retry_count", max(e.Attempts-1, 0))}
			if e.Status != 0 {
				attrs = append(attrs, attribute.Int("http.response.status_code", e.Status))
			}
			if e.Code != "" {
				attrs = append(attrs, attribute.String("bizgo.code", e.Code), attribute.String("bizgo.layer", string(e.Layer)))
			}
			span.SetAttributes(attrs...)
			if !e.Success {
				span.SetStatus(codes.Error, e.ErrorKind)
				span.SetAttributes(attribute.String("error.type", e.ErrorKind))
			}
			span.End(trace.WithTimestamp(e.Started.Add(e.Duration)))
		},
	}
}

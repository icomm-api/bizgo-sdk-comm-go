package bizgo

import (
	"context"
	"errors"
	"time"
)

// Hooks observe API calls (SDK-DESIGN.md §11.4), for metrics and tracing. Pass them with [WithHooks].
// The OpenTelemetry adapter (module github.com/icomm-api/bizgo-sdk-comm-go/otel) is built on them.
//
// One API call, including its retries, produces one OnRequestStart and one OnRequestEnd. A
// [RequestEvent] carries the operation, the HTTP method, the path template, the status, the error
// layer and code, the number of attempts and the duration. It never carries request or response
// bodies, query strings, header values, real path values, phone numbers or the API key.
//
// Both functions are optional. A panic in a hook is recovered and ignored, so observability can
// never change a result, a retry or a bulk send. Hooks run in the calling goroutine: keep them fast.
type Hooks struct {
	// OnRequestStart is called before the first attempt; only the operation fields of the event are
	// set. The context it returns (nil: unchanged) is passed to OnRequestEnd, so a tracer can keep
	// its span there; it is not used for the HTTP request.
	OnRequestStart func(ctx context.Context, e RequestEvent) context.Context
	// OnRequestEnd is called after the call finished, successfully or not, after all retries.
	OnRequestEnd func(ctx context.Context, e RequestEvent)
}

// RequestEvent is what a hook may see about one API call.
type RequestEvent struct {
	// OperationID is the operationId of the spec, for example "sendOmni".
	OperationID string
	// Operation is "<resource>.<method>", for example "send.omni" or "alimtalk.templates.list".
	Operation string
	// Method is the HTTP method.
	Method string
	// PathTemplate is the path template, for example "/api/comm/v1/report/inquiry/{msgKey}".
	PathTemplate string
	// Status is the HTTP status of the last attempt (0 after a network error or before the end).
	Status int
	// Layer and Code are set when the call failed with an API error code.
	Layer Layer
	Code  string
	// ErrorKind is the kind of the error (for example "rate limit", "timeout", "invalid response"),
	// empty on success.
	ErrorKind string
	// Success reports whether the call succeeded (including decoding the response).
	Success bool
	// Attempts is the number of HTTP attempts (1 = no retry).
	Attempts int
	// Duration is the time from the start to the end of the call.
	Duration time.Duration
	// Started is when the call started.
	Started time.Time
}

type hookCall struct {
	ev   RequestEvent
	ctxs []context.Context
}

func (t *transport) hookStart(ctx context.Context, r request) *hookCall {
	if len(t.hooks) == 0 || r.op == nil {
		return nil
	}
	hc := &hookCall{ev: RequestEvent{
		OperationID: r.op.id, Operation: r.op.name, Method: r.op.method, PathTemplate: r.op.path, Started: time.Now(),
	}}
	hc.ctxs = make([]context.Context, len(t.hooks))
	for i, h := range t.hooks {
		hc.ctxs[i] = ctx
		if h.OnRequestStart != nil {
			if next := safeStart(h.OnRequestStart, ctx, hc.ev); next != nil {
				hc.ctxs[i] = next
			}
		}
	}
	return hc
}

func safeStart(fn func(context.Context, RequestEvent) context.Context, ctx context.Context, e RequestEvent) (out context.Context) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return fn(ctx, e)
}

func (t *transport) hookEnd(hc *hookCall, status, attempts int, err error) {
	if hc == nil {
		return
	}
	ev := hc.ev
	ev.Status, ev.Attempts, ev.Duration, ev.Success = status, attempts, time.Since(ev.Started), err == nil
	if err != nil {
		ev.ErrorKind = errorKindName(err)
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			ev.Layer, ev.Code, ev.Status = apiErr.Layer, apiErr.Code, apiErr.HTTPStatus
		}
		var invalid *InvalidResponseError
		if errors.As(err, &invalid) && invalid.HTTPStatus != 0 {
			ev.Status = invalid.HTTPStatus
		}
	}
	for i, h := range t.hooks {
		if h.OnRequestEnd != nil {
			safeEnd(h.OnRequestEnd, hc.ctxs[i], ev)
		}
	}
}

func safeEnd(fn func(context.Context, RequestEvent), ctx context.Context, e RequestEvent) {
	defer func() { _ = recover() }()
	fn(ctx, e)
}

// errorKindName is a value-free name of an error kind.
func errorKindName(err error) string {
	for _, k := range errorKinds {
		if errors.Is(err, k.kind) {
			return k.name
		}
	}
	return "error"
}

package bizgo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook security notes:
//
//   - The signature is X-IB-Signature = HmacSHA256(secret, X-IB-Timestamp); the receiver
//     verifies it together with the timestamp window. As general practice, also serve the endpoint
//     over HTTPS, allow only the Bizgo webhook source IPs, and confirm important results with the
//     inquiry APIs.
//   - Report and MO webhooks always need a valid signature.
//   - Counsel talk (상담톡) webhooks have no signature (signatures apply only to report and MO
//     webhooks). The SDK does not require or check one and ignores the signature headers if they
//     are present; the body checks (1MB, JSON depth 64, field types) apply as for every webhook.
//     The package-level ParseCounsel*Webhook functions need no receiver and no webhook secret.
//   - Bizgo retries a webhook up to 3 times when it does not get the documented answer within
//     5 seconds, so deduplicate (by msgKey).
//   - The signature output encoding (hex or base64) is not confirmed yet; both are accepted.
//   - The webhook secret is obtained by requesting it from Bizgo (it is delivered separately).

const (
	// WebhookTimestampHeader carries the send time of a webhook (epoch milliseconds or seconds).
	WebhookTimestampHeader = "X-IB-Timestamp"
	// WebhookSignatureHeader carries HMAC-SHA256(secret, timestamp).
	WebhookSignatureHeader = "X-IB-Signature"
	// DefaultWebhookTolerance is the maximum age of a webhook timestamp, to limit replays.
	DefaultWebhookTolerance = 5 * time.Minute
	// NoWebhookTolerance, passed as tolerance to [VerifyWebhookSignature], disables the timestamp age
	// check (not recommended: a captured request can then be replayed forever).
	NoWebhookTolerance time.Duration = -1
	// MaxWebhookBodyBytes is the largest webhook body accepted.
	MaxWebhookBodyBytes = 1 << 20
)

func webhookError(msg string) error { return &WebhookVerificationError{msg: msg} }

// VerifyWebhookSignature checks X-IB-Signature against HMAC-SHA256(secret, timestamp) in constant
// time and checks that the timestamp is within tolerance of now. The signature may be hex (any
// case) or base64. The timestamp must be 1 to 16 ASCII digits:
// epoch milliseconds when it has 13 or more digits, otherwise epoch seconds.
//
// tolerance must be greater than 0, or [NoWebhookTolerance] to disable the age check. A secret that
// is empty or only white space is rejected.
func VerifyWebhookSignature(secret []byte, timestamp, signature string, tolerance time.Duration, now time.Time) error {
	if len(bytes.TrimSpace(secret)) == 0 {
		return webhookError("웹훅 secret이 비어 있습니다")
	}
	if tolerance <= 0 && tolerance != NoWebhookTolerance {
		return webhookError("tolerance는 0보다 커야 합니다(검사를 끄려면 bizgo.NoWebhookTolerance)")
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return webhookError("X-IB-Signature 헤더가 없습니다")
	}
	sentAt, err := webhookTime(timestamp)
	if err != nil {
		return err
	}
	if tolerance > 0 {
		age := now.Sub(sentAt)
		if age > tolerance || age < -tolerance {
			return webhookError("X-IB-Timestamp가 허용 범위(" + strconv.Itoa(int(tolerance.Seconds())) + "초)를 벗어났습니다")
		}
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	want := mac.Sum(nil)

	received := signature
	var hexOK, b64OK bool
	if decoded, err := hex.DecodeString(received); err == nil {
		hexOK = hmac.Equal(decoded, want)
	}
	if decoded, err := base64.StdEncoding.DecodeString(received); err == nil {
		b64OK = hmac.Equal(decoded, want)
	}
	if !hexOK && !b64OK {
		return webhookError("서명이 일치하지 않습니다")
	}
	return nil
}

// webhookTime parses 1 to 16 ASCII digits.
func webhookTime(ts string) (time.Time, error) {
	if ts == "" || len(ts) > 16 || strings.Trim(ts, "0123456789") != "" {
		return time.Time{}, webhookError("X-IB-Timestamp는 1~16자리 숫자여야 합니다")
	}
	v, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Time{}, webhookError("X-IB-Timestamp는 1~16자리 숫자여야 합니다")
	}
	if len(ts) >= 13 {
		return time.UnixMilli(v), nil
	}
	return time.Unix(v, 0), nil
}

// WebhookReceiver verifies and parses Bizgo webhooks with one secret. It is safe for concurrent use.
// The secret is used for report and MO webhooks; the counsel talk methods do not check a signature
// (counsel webhooks have none). Code that only receives counsel webhooks needs no secret: use the
// package-level ParseCounsel*Webhook functions and [NewCounselWebhookAck].
//
//	receiver, err := bizgo.NewWebhookReceiver([]byte(os.Getenv("BIZGO_WEBHOOK_SECRET")))
//	http.Handle("/bizgo/report", receiver.ReportHandler(func(ctx context.Context, r *bizgo.ReportWebhookPayload) error {
//		return store(ctx, r) // make it idempotent: the same report can arrive again
//	}))
type WebhookReceiver struct {
	secret         []byte
	tolerance      time.Duration
	now            func() time.Time
	configProblems []string
}

// WebhookOption configures a [WebhookReceiver].
type WebhookOption func(*WebhookReceiver)

// WithWebhookTolerance sets the maximum timestamp age (default 5 minutes). It must be greater than
// 0; use [NoWebhookTolerance] to disable the check (not recommended).
func WithWebhookTolerance(d time.Duration) WebhookOption {
	return func(r *WebhookReceiver) {
		if d <= 0 && d != NoWebhookTolerance {
			r.configProblems = append(r.configProblems, "tolerance는 0보다 커야 합니다(검사를 끄려면 bizgo.NoWebhookTolerance)")
			return
		}
		r.tolerance = d
	}
}

// NewWebhookReceiver creates a receiver. The secret is copied. Read it from an environment
// variable or a secret store; request it from Bizgo.
func NewWebhookReceiver(secret []byte, opts ...WebhookOption) (*WebhookReceiver, error) {
	if len(bytes.TrimSpace(secret)) == 0 {
		return nil, &ConfigurationError{msg: "웹훅 secret이 비어 있습니다"}
	}
	r := &WebhookReceiver{secret: bytes.Clone(secret), tolerance: DefaultWebhookTolerance, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	if len(r.configProblems) > 0 {
		return nil, &ConfigurationError{msg: strings.Join(r.configProblems, "; ")}
	}
	return r, nil
}

// String describes the receiver without the secret.
func (r *WebhookReceiver) String() string {
	return "bizgo.WebhookReceiver(tolerance=" + r.tolerance.String() + ")"
}

// GoString describes the receiver without the secret.
func (r *WebhookReceiver) GoString() string { return r.String() }

// header reads a header case-insensitively, also for maps built by hand.
func header(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// Verify checks the signature headers of a request.
func (r *WebhookReceiver) Verify(h http.Header) error {
	return VerifyWebhookSignature(r.secret, header(h, WebhookTimestampHeader), header(h, WebhookSignatureHeader),
		r.tolerance, r.now())
}

// Report verifies the headers, then parses a delivery report webhook body.
func (r *WebhookReceiver) Report(h http.Header, body []byte) (*ReportWebhookPayload, error) {
	if err := r.Verify(h); err != nil {
		return nil, err
	}
	return ParseReportWebhook(body)
}

// MO verifies the headers, then parses an MO webhook body.
func (r *WebhookReceiver) MO(h http.Header, body []byte) (*MOWebhookPayload, error) {
	if err := r.Verify(h); err != nil {
		return nil, err
	}
	return ParseMOWebhook(body)
}

// ParseReportWebhook parses a report webhook body without verifying the signature.
func ParseReportWebhook(body []byte) (*ReportWebhookPayload, error) {
	return parseWebhook[ReportWebhookPayload](body)
}

// ParseMOWebhook parses an MO webhook body without verifying the signature.
func ParseMOWebhook(body []byte) (*MOWebhookPayload, error) {
	return parseWebhook[MOWebhookPayload](body)
}

// parseWebhook turns every problem of the body (size, not JSON, nested too deep, a field of the
// wrong type) into a *WebhookVerificationError, so that handlers answer 4xx.
func parseWebhook[T any](body []byte) (*T, error) {
	if len(body) > MaxWebhookBodyBytes {
		return nil, webhookError("웹훅 본문이 너무 큽니다")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, webhookError("웹훅 본문이 JSON 객체가 아닙니다")
	}
	if !jsonDepthOK(trimmed, maxJSONDepth) {
		return nil, webhookError("웹훅 본문의 중첩이 너무 깊습니다")
	}
	var v T
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return nil, webhookError("웹훅 본문이 JSON이 아니거나 형식이 다릅니다")
	}
	return &v, nil
}

// NewWebhookAck returns the response body Bizgo expects for report and MO webhooks:
// {"msgKey": "<received msgKey>"}.
func NewWebhookAck(msgKey string) WebhookAck { return WebhookAck{MsgKey: msgKey} }

// NewCounselWebhookAck returns the response body Bizgo expects for counsel talk webhooks:
// {"code": "A000", "result": "Success"}.
func NewCounselWebhookAck() CounselWebhookAck {
	return CounselWebhookAck{Code: "A000", Result: "Success"}
}

// ReportHandler returns an http.Handler for report webhooks. It reads at most 1MB, verifies the
// signature (401 on failure), parses the body (400 on failure), calls fn, and answers
// {"msgKey": ...}. If fn returns an error (or panics) the handler answers 500 without the ack, so
// Bizgo sends the report again.
func (r *WebhookReceiver) ReportHandler(fn func(context.Context, *ReportWebhookPayload) error) http.Handler {
	return webhookHandler(r.Verify, ParseReportWebhook, fn, func(p *ReportWebhookPayload) any { return NewWebhookAck(p.MsgKey) })
}

// MOHandler is [WebhookReceiver.ReportHandler] for MO webhooks.
func (r *WebhookReceiver) MOHandler(fn func(context.Context, *MOWebhookPayload) error) http.Handler {
	return webhookHandler(r.Verify, ParseMOWebhook, fn, func(p *MOWebhookPayload) any { return NewWebhookAck(p.MsgKey) })
}

// webhookInfo describes a webhook of the spec (generated table, used by tests).
type webhookInfo struct {
	name, id            string
	signed, handwritten bool
}

// webhookHandler serves one webhook; verify is nil for the webhooks Bizgo does not sign (counsel talk).
func webhookHandler[T any](verify func(http.Header) error, parse func([]byte) (*T, error), fn func(context.Context, *T) error, ack func(*T) any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if verify != nil {
			if err := verify(req.Header); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxWebhookBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		payload, err := parse(body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if fn != nil {
			if err := callWebhook(req.Context(), fn, payload); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ack(payload))
	})
}

var errHandlerPanic = errors.New("bizgo: webhook handler panicked")

// callWebhook runs the user's function; a panic becomes an error (500, so Bizgo sends it again).
func callWebhook[T any](ctx context.Context, fn func(context.Context, *T) error, payload *T) (err error) {
	defer func() {
		if recover() != nil {
			err = errHandlerPanic
		}
	}()
	return fn(ctx, payload)
}

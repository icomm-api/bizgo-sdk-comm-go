package bizgo

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Error kinds. Test an error with [errors.Is]:
//
//	if errors.Is(err, bizgo.ErrRateLimit) { ... }
//
// An [*APIError] matches exactly one of the API kinds below. A [*ConnectionError] matches
// ErrConnection, and also ErrTimeout when it timed out.
var (
	ErrBadRequest       = errors.New("bizgo: bad request")         // HTTP 400, gateway A400, most service codes
	ErrAuthentication   = errors.New("bizgo: authentication")      // HTTP 401, gateway A401, service A001/A002/A100
	ErrPermissionDenied = errors.New("bizgo: permission denied")   // HTTP 403, gateway A403, service A110/A111
	ErrNotFound         = errors.New("bizgo: not found")           // HTTP 404, gateway A404
	ErrDuplicateRequest = errors.New("bizgo: duplicate request")   // service A301 as data.code (request level; per-recipient A301 is SendResult.Duplicates)
	ErrRateLimit        = errors.New("bizgo: rate limit")          // HTTP 429, service A020
	ErrInternalServer   = errors.New("bizgo: internal server")     // HTTP 5xx
	ErrAPI              = errors.New("bizgo: api error")           // any other failure code
	ErrConnection       = errors.New("bizgo: connection error")    // no response received
	ErrTimeout          = errors.New("bizgo: timeout")             // the request timed out
	ErrInvalidResponse  = errors.New("bizgo: invalid response")    // not the documented envelope
	ErrValidation       = errors.New("bizgo: validation error")    // request rejected before sending
	ErrWebhook          = errors.New("bizgo: webhook rejected")    // webhook signature/body check failed
	ErrConfiguration    = errors.New("bizgo: configuration error") // client options are invalid
)

// Layer is where a failure code came from. The same code can mean different things in each
// layer: gateway A401 is an authentication failure, service A401 is an invalid paymentCode.
type Layer string

const (
	// LayerGateway: authentication, permission and format checks (common.authCode).
	LayerGateway Layer = "gateway"
	// LayerService: the product API (data.code).
	LayerService Layer = "service"
)

// APIError is a failure returned by Bizgo.
//
// Its Error text never contains the API key, the request body or phone numbers.
// Body, however, is the raw response and can contain phone numbers: do not log it as is.
type APIError struct {
	HTTPStatus  int    // HTTP status code
	Code        string // common.authCode (gateway) or data.code (service), for example "A401"
	Layer       Layer  // LayerGateway or LayerService
	Message     string // authResult / result text from the server (or an SDK explanation)
	Description string // Korean description of a service code from the error code table, if known
	TrackingID  string // common.infobankTrId; give it to Bizgo support when asking about a request
	// RetryAfter is the Retry-After header of a rate-limited response (0 if absent).
	RetryAfter time.Duration
	// AlreadyAccepted is set on ErrDuplicateRequest (A301) after a retry: an earlier attempt of the
	// same request (same idempotency key) was accepted, so nothing was sent twice.
	AlreadyAccepted bool
	// Body is the raw response body. It can contain phone numbers.
	Body []byte

	kind error
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("HTTP %d", e.HTTPStatus), fmt.Sprintf("%s code=%s", e.Layer, e.Code)}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Description != "" && e.Description != e.Message {
		parts = append(parts, e.Description)
	}
	if e.TrackingID != "" {
		parts = append(parts, "infobankTrId="+e.TrackingID)
	}
	return strings.Join(parts, " | ")
}

// GoString keeps %#v from printing Body.
func (e *APIError) GoString() string {
	return fmt.Sprintf("&bizgo.APIError{HTTPStatus:%d, Code:%q, Layer:%q, TrackingID:%q}",
		e.HTTPStatus, e.Code, e.Layer, e.TrackingID)
}

// Is reports whether target is the kind of this error (ErrRateLimit, ErrAuthentication, ...).
func (e *APIError) Is(target error) bool { return target == e.kind }

// Kind returns the error kind (ErrBadRequest, ErrAuthentication, ...).
func (e *APIError) Kind() error { return e.kind }

// ConnectionError means no response was received (connection failure or timeout).
//
// For a send, the message may or may not have been accepted: re-sending can deliver it twice
// unless you set an idempotency key. Check with the status APIs.
type ConnectionError struct {
	Timeout bool
	msg     string
	cause   error // never a *url.Error: that one contains the full URL with the query string
}

func (e *ConnectionError) Error() string { return e.msg }

// Is matches ErrConnection, and ErrTimeout when the request timed out.
func (e *ConnectionError) Is(target error) bool {
	return target == ErrConnection || (e.Timeout && target == ErrTimeout)
}

// Unwrap returns the underlying network error or context error (without the request URL).
func (e *ConnectionError) Unwrap() error { return e.cause }

// InvalidResponseError means the response was not the documented {common, data} envelope, could
// not be decoded, was too large, or was a redirect (never followed).
//
// For a send it can mean the request was accepted: check with the status APIs before sending again.
type InvalidResponseError struct {
	HTTPStatus int
	// TrackingID is common.infobankTrId when the envelope could be read.
	TrackingID string
	// Body is the raw response body (nil for redirects and oversized bodies). It can contain phone numbers.
	Body []byte
	msg  string
}

func (e *InvalidResponseError) Error() string { return e.msg }

// Is matches ErrInvalidResponse.
func (e *InvalidResponseError) Is(target error) bool { return target == ErrInvalidResponse }

// GoString keeps %#v from printing Body.
func (e *InvalidResponseError) GoString() string {
	return fmt.Sprintf("&bizgo.InvalidResponseError{HTTPStatus:%d, TrackingID:%q}", e.HTTPStatus, e.TrackingID)
}

// ConfigurationError means the client options are invalid (missing API key, insecure base URL, ...).
type ConfigurationError struct{ msg string }

func (e *ConfigurationError) Error() string { return e.msg }

// Is matches ErrConfiguration.
func (e *ConfigurationError) Is(target error) bool { return target == ErrConfiguration }

// WebhookVerificationError means a webhook request failed the signature, timestamp or body
// check. Answer it with HTTP 401.
type WebhookVerificationError struct{ msg string }

func (e *WebhookVerificationError) Error() string { return e.msg }

// Is matches ErrWebhook.
func (e *WebhookVerificationError) Is(target error) bool { return target == ErrWebhook }

type serviceCode struct {
	httpStatus  int
	description string
}

// Code tables per layer: the same code means different things in each layer.
var (
	gatewayKinds = map[string]error{
		"A400": ErrBadRequest,
		"A401": ErrAuthentication,
		"A403": ErrPermissionDenied,
		"A404": ErrNotFound,
	}
	serviceKinds = map[string]error{
		"A001": ErrAuthentication,
		"A002": ErrAuthentication,
		"A100": ErrAuthentication,
		"A110": ErrPermissionDenied,
		"A111": ErrPermissionDenied,
		"A020": ErrRateLimit,
		"A301": ErrDuplicateRequest,
	}
	statusKinds = map[int]error{
		400: ErrBadRequest,
		401: ErrAuthentication,
		403: ErrPermissionDenied,
		404: ErrNotFound,
		429: ErrRateLimit,
	}
)

// errorKind picks the kind: the layer's code table first, then the HTTP status. A service failure
// inside an HTTP 200 uses the status documented for its code (A306 -> 400 -> ErrBadRequest).
func errorKind(status int, code string, layer Layer) error {
	table := gatewayKinds
	if layer == LayerService {
		table = serviceKinds
	}
	if kind, ok := table[code]; ok {
		return kind
	}
	if layer == LayerService && status < 400 {
		if known, ok := serviceCodes[code]; ok && known.httpStatus != 0 {
			status = known.httpStatus
		}
	}
	if kind, ok := statusKinds[status]; ok {
		return kind
	}
	switch {
	case status >= 500:
		return ErrInternalServer
	case status >= 400:
		return ErrBadRequest
	}
	return ErrAPI
}

func newAPIError(status int, code string, layer Layer, message, trackingID string, body []byte) *APIError {
	e := &APIError{
		HTTPStatus: status,
		Code:       code,
		Layer:      layer,
		Message:    message,
		TrackingID: trackingID,
		Body:       body,
		kind:       errorKind(status, code, layer),
	}
	if layer == LayerService {
		if known, ok := serviceCodes[code]; ok {
			e.Description = known.description
		}
	}
	return e
}

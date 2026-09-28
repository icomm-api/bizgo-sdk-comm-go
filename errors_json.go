package bizgo

import (
	"encoding/json"
	"errors"
	"time"
)

// Errors survive an encoding/json round trip (SDK-DESIGN.md §12.10), so they can be put in a job
// queue or sent to another process: errors.Is still matches the same kind after decoding. The
// wrapped network error of a ConnectionError is not encoded (only its message and Timeout).

// errorKinds names the kinds, in the order errors.Is is tried (ErrTimeout before ErrConnection).
var errorKinds = []struct {
	name string
	kind error
}{
	{"bad request", ErrBadRequest}, {"authentication", ErrAuthentication}, {"permission denied", ErrPermissionDenied},
	{"not found", ErrNotFound}, {"duplicate request", ErrDuplicateRequest}, {"rate limit", ErrRateLimit},
	{"internal server", ErrInternalServer}, {"api error", ErrAPI}, {"timeout", ErrTimeout},
	{"connection error", ErrConnection}, {"invalid response", ErrInvalidResponse}, {"validation error", ErrValidation},
	{"webhook rejected", ErrWebhook}, {"configuration error", ErrConfiguration}, {"panic", ErrChunkPanic},
}

func kindByName(name string) error {
	for _, k := range errorKinds {
		if k.name == name {
			return k.kind
		}
	}
	return nil
}

type apiErrorJSON struct {
	Kind            string `json:"kind"`
	HTTPStatus      int    `json:"httpStatus"`
	Code            string `json:"code,omitempty"`
	Layer           Layer  `json:"layer,omitempty"`
	Message         string `json:"message,omitempty"`
	Description     string `json:"description,omitempty"`
	TrackingID      string `json:"trackingId,omitempty"`
	RetryAfterMs    int64  `json:"retryAfterMs,omitempty"`
	AlreadyAccepted bool   `json:"alreadyAccepted,omitempty"`
	Body            []byte `json:"body,omitempty"`
}

// MarshalJSON encodes the error with its kind. Body (which can contain phone numbers) is included:
// protect the encoded value like the response itself.
func (e *APIError) MarshalJSON() ([]byte, error) {
	return json.Marshal(apiErrorJSON{
		Kind: errorKindName(e), HTTPStatus: e.HTTPStatus, Code: e.Code, Layer: e.Layer, Message: e.Message,
		Description: e.Description, TrackingID: e.TrackingID, RetryAfterMs: e.RetryAfter.Milliseconds(),
		AlreadyAccepted: e.AlreadyAccepted, Body: e.Body,
	})
}

// UnmarshalJSON restores an error encoded by MarshalJSON, including its kind.
func (e *APIError) UnmarshalJSON(data []byte) error {
	var v apiErrorJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	kind := kindByName(v.Kind)
	if kind == nil {
		kind = errorKind(v.HTTPStatus, v.Code, v.Layer)
	}
	*e = APIError{
		HTTPStatus: v.HTTPStatus, Code: v.Code, Layer: v.Layer, Message: v.Message, Description: v.Description,
		TrackingID: v.TrackingID, RetryAfter: time.Duration(v.RetryAfterMs) * time.Millisecond,
		AlreadyAccepted: v.AlreadyAccepted, Body: v.Body, kind: kind,
	}
	return nil
}

type connectionErrorJSON struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Timeout bool   `json:"timeout,omitempty"`
}

// MarshalJSON encodes the message and Timeout (not the wrapped network error).
func (e *ConnectionError) MarshalJSON() ([]byte, error) {
	return json.Marshal(connectionErrorJSON{Kind: errorKindName(e), Message: e.msg, Timeout: e.Timeout})
}

// UnmarshalJSON restores an error encoded by MarshalJSON.
func (e *ConnectionError) UnmarshalJSON(data []byte) error {
	var v connectionErrorJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*e = ConnectionError{Timeout: v.Timeout, msg: v.Message, cause: errors.New(v.Message)}
	return nil
}

type invalidResponseJSON struct {
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	TrackingID string `json:"trackingId,omitempty"`
	Body       []byte `json:"body,omitempty"`
}

// MarshalJSON encodes the error. Body can contain phone numbers.
func (e *InvalidResponseError) MarshalJSON() ([]byte, error) {
	return json.Marshal(invalidResponseJSON{Kind: "invalid response", Message: e.msg, HTTPStatus: e.HTTPStatus,
		TrackingID: e.TrackingID, Body: e.Body})
}

// UnmarshalJSON restores an error encoded by MarshalJSON.
func (e *InvalidResponseError) UnmarshalJSON(data []byte) error {
	var v invalidResponseJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*e = InvalidResponseError{HTTPStatus: v.HTTPStatus, TrackingID: v.TrackingID, Body: v.Body, msg: v.Message}
	return nil
}

type messageErrorJSON struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// MarshalJSON encodes the message.
func (e *ConfigurationError) MarshalJSON() ([]byte, error) {
	return json.Marshal(messageErrorJSON{"configuration error", e.msg})
}

// UnmarshalJSON restores an error encoded by MarshalJSON.
func (e *ConfigurationError) UnmarshalJSON(data []byte) error {
	var v messageErrorJSON
	err := json.Unmarshal(data, &v)
	e.msg = v.Message
	return err
}

// MarshalJSON encodes the message.
func (e *WebhookVerificationError) MarshalJSON() ([]byte, error) {
	return json.Marshal(messageErrorJSON{"webhook rejected", e.msg})
}

// UnmarshalJSON restores an error encoded by MarshalJSON.
func (e *WebhookVerificationError) UnmarshalJSON(data []byte) error {
	var v messageErrorJSON
	err := json.Unmarshal(data, &v)
	e.msg = v.Message
	return err
}

// Gob (encoding/gob) uses the same encoding as JSON, so that errors keep their kind across
// processes with either encoding.

// GobEncode implements gob.GobEncoder.
func (e *APIError) GobEncode() ([]byte, error) { return e.MarshalJSON() }

// GobDecode implements gob.GobDecoder.
func (e *APIError) GobDecode(data []byte) error { return e.UnmarshalJSON(data) }

// GobEncode implements gob.GobEncoder.
func (e *ConnectionError) GobEncode() ([]byte, error) { return e.MarshalJSON() }

// GobDecode implements gob.GobDecoder.
func (e *ConnectionError) GobDecode(data []byte) error { return e.UnmarshalJSON(data) }

// GobEncode implements gob.GobEncoder.
func (e *InvalidResponseError) GobEncode() ([]byte, error) { return e.MarshalJSON() }

// GobDecode implements gob.GobDecoder.
func (e *InvalidResponseError) GobDecode(data []byte) error { return e.UnmarshalJSON(data) }

// GobEncode implements gob.GobEncoder.
func (e *ConfigurationError) GobEncode() ([]byte, error) { return e.MarshalJSON() }

// GobDecode implements gob.GobDecoder.
func (e *ConfigurationError) GobDecode(data []byte) error { return e.UnmarshalJSON(data) }

// GobEncode implements gob.GobEncoder.
func (e *WebhookVerificationError) GobEncode() ([]byte, error) { return e.MarshalJSON() }

// GobDecode implements gob.GobDecoder.
func (e *WebhookVerificationError) GobDecode(data []byte) error { return e.UnmarshalJSON(data) }

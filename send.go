package bizgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

// DefaultIdempotencyTTL is the idempotencyTtl (seconds, 24 hours; the API maximum) that the SDK sends
// when a request has an idempotency key but no TTL. The API rejects a key without a TTL (A309), so
// every request body with both fields (Send.*, Send.Bulk chunks, generated operations) gets this
// value when IdempotencyTTL is nil. An explicit TTL, 0 included, is sent as given, and without a key
// no TTL is added. The caller's request value is never modified.
const DefaultIdempotencyTTL = 86400

// SendService sends messages. Use it as client.Send.
type SendService struct{ t *transport }

// OmniParams are the parameters of [SendService.Omni].
type OmniParams struct {
	// To are the recipients (max 200). Use [To] for plain phone numbers, or Destination values
	// for per-recipient ReplaceWords and Ref.
	To []Destination
	// Messages are the channel messages in fallback order: if the first fails, the next is sent.
	// For example: []bizgo.ChannelMessage{&bizgo.AlimtalkMessage{...}, &bizgo.SMSMessage{...}}.
	Messages []ChannelMessage
	// Ref is your reference value; it is returned in reports.
	Ref string
	// GroupKey groups messages in message insight statistics.
	GroupKey string
	// PaymentCode is a department code for billing.
	PaymentCode string
	// IdempotencyKey (max 200 chars) makes retries safe: a resend with the same key within
	// IdempotencyTTL seconds is not delivered twice: the recipients accepted earlier come back in
	// [SendResult.Duplicates] (per-recipient A301), not in Failed. A request-level A301 is
	// ErrDuplicateRequest. It also enables automatic retries after timeouts and server errors.
	IdempotencyKey string
	// IdempotencyTTL is how many seconds (0-86400) the key is remembered. Nil with an
	// IdempotencyKey: [DefaultIdempotencyTTL] (86400) is sent, because the API rejects a key
	// without a TTL (A309). Nil without a key: not sent. An explicit value, 0 included, is sent as is.
	IdempotencyTTL *int
}

// Omni sends one message, with optional fallback channels, to up to 200 recipients.
//
// The request is validated before anything is sent; problems are returned as a
// [*ValidationError]. The result only means the message was accepted: check
// [SendResult.Failed] (and [SendResult.Duplicates] when you resend with the same IdempotencyKey), and
// get the delivery result from reports.
//
// A [*ConnectionError] means the result is unknown: the message may have been accepted.
func (s *SendService) Omni(ctx context.Context, p OmniParams) (*SendResult, error) {
	if len(p.Messages) == 0 {
		return nil, invalid("messageFlow", "메시지가 없습니다. 채널 메시지를 1개 이상 넣으세요")
	}
	flow := make([]MessageFlowItem, len(p.Messages))
	for i, m := range p.Messages {
		if m == nil {
			return nil, invalid(indexPath("messageFlow", i), "nil 메시지입니다")
		}
		flow[i] = m.flowItem()
	}
	return s.Request(ctx, &SendOmniRequest{
		Destinations:   p.To,
		MessageFlow:    flow,
		Ref:            p.Ref,
		GroupKey:       p.GroupKey,
		PaymentCode:    p.PaymentCode,
		IdempotencyKey: p.IdempotencyKey,
		IdempotencyTTL: p.IdempotencyTTL,
	})
}

// Request sends a prepared request body. Use [ParseSendOmniRequest] to build one from JSON.
//
// With an IdempotencyKey and a nil IdempotencyTTL, [DefaultIdempotencyTTL] is sent; req itself is
// not modified.
func (s *SendService) Request(ctx context.Context, req *SendOmniRequest) (*SendResult, error) {
	if req == nil {
		return nil, invalid("", "요청이 nil입니다")
	}
	req = req.withDefaultIdempotencyTTL()
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, invalid("", "요청을 JSON으로 만들 수 없습니다")
	}
	policy := retryRateLimitOnly // a retried send after a timeout could deliver twice
	if req.IdempotencyKey != "" {
		policy = retrySafe
	}
	resp, err := call[SendOmniResponse](ctx, s.tr(), request{
		op: opSendOmni, method: "POST", path: opSendOmni.path, body: body, contentType: "application/json",
		policy: policy, cost: len(req.Destinations),
	})
	if err != nil {
		return nil, err
	}
	result := &SendResult{}
	if resp.Common != nil {
		result.TrackingID = resp.Common.InfobankTrID
	}
	if resp.Data != nil {
		result.Ref = resp.Data.Ref
		if resp.Data.Data != nil {
			result.Destinations = resp.Data.Data.Destinations
		}
	}
	return result, nil
}

// SMSParams are the parameters of [SendService.SMS].
type SMSParams struct {
	To             []Destination
	From           string // a sender number registered in the Bizgo console
	Text           string // max 90 bytes in EUC-KR (about 45 Korean characters)
	Ref            string
	IdempotencyKey string
}

// SMS sends an SMS.
func (s *SendService) SMS(ctx context.Context, p SMSParams) (*SendResult, error) {
	return s.Omni(ctx, OmniParams{
		To:             p.To,
		Messages:       []ChannelMessage{&SMSMessage{From: p.From, Text: p.Text}},
		Ref:            p.Ref,
		IdempotencyKey: p.IdempotencyKey,
	})
}

// LMSParams are the parameters of [SendService.LMS].
type LMSParams struct {
	To             []Destination
	From           string
	Text           string // max 2,000 bytes in EUC-KR
	Title          string
	Ref            string
	IdempotencyKey string
}

// LMS sends an LMS (long text message).
func (s *SendService) LMS(ctx context.Context, p LMSParams) (*SendResult, error) {
	return s.Omni(ctx, OmniParams{
		To:             p.To,
		Messages:       []ChannelMessage{&MMSMessage{From: p.From, Text: p.Text, Title: p.Title}},
		Ref:            p.Ref,
		IdempotencyKey: p.IdempotencyKey,
	})
}

// MMSParams are the parameters of [SendService.MMS].
type MMSParams struct {
	To             []Destination
	From           string
	Text           string
	Title          string
	FileKeys       []string // from FilesService.UploadMMS, max 3
	Ref            string
	IdempotencyKey string
}

// MMS sends an MMS. Upload the images with [FilesService.UploadMMS] first.
func (s *SendService) MMS(ctx context.Context, p MMSParams) (*SendResult, error) {
	keys := p.FileKeys
	if keys == nil {
		keys = []string{} // an MMS without images is rejected (minItems 1) instead of sent as LMS
	}
	return s.Omni(ctx, OmniParams{
		To:             p.To,
		Messages:       []ChannelMessage{&MMSMessage{From: p.From, Text: p.Text, Title: p.Title, FileKey: keys}},
		Ref:            p.Ref,
		IdempotencyKey: p.IdempotencyKey,
	})
}

// Validate checks the request against the spec (required fields, lengths, EUC-KR byte limits,
// item counts, allowed values, one channel per message flow item). It returns a
// [*ValidationError] listing field paths and reasons, never the values.
func (r *SendOmniRequest) Validate() error {
	v := &validator{}
	r.validate(v, "")
	return v.err()
}

// ParseSendOmniRequest decodes a send request body written with the API field names and
// validates it. Unknown fields are rejected, and field names are matched exactly (encoding/json
// alone would accept "templatecode" for "templateCode"), so typos fail before sending.
func ParseSendOmniRequest(data []byte) (*SendOmniRequest, error) {
	var generic any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, invalid("", decodeReason(err))
	}
	if dec.More() {
		return nil, invalid("", "JSON 값 뒤에 다른 내용이 있습니다")
	}
	v := &validator{}
	checkKeys(v, generic, reflect.TypeFor[SendOmniRequest](), "")
	if err := v.err(); err != nil {
		return nil, err
	}
	var req SendOmniRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, invalid("", decodeReason(err))
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return &req, nil
}

// checkKeys reports object keys that are not exactly a JSON field name of t.
func checkKeys(v *validator, value any, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return // type errors are reported by json.Unmarshal
		}
		fields := map[string]reflect.Type{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.IsExported() && name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, ok := fields[k]
			if !ok {
				v.add(joinPath(path, k), "알 수 없는 필드입니다")
				continue
			}
			checkKeys(v, obj[k], ft, joinPath(path, k))
		}
	case reflect.Slice:
		if arr, ok := value.([]any); ok {
			for i, item := range arr {
				checkKeys(v, item, t.Elem(), indexPath(path, i))
			}
		}
	}
}

// decodeReason describes a request decode error without echoing input values.
func decodeReason(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		if te.Field != "" {
			return "타입이 맞지 않습니다: " + te.Field
		}
		return "타입이 맞지 않습니다"
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "JSON 구문 오류입니다"
	}
	return "JSON을 해석할 수 없습니다"
}

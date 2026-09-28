package bizgo

import (
	"encoding/json"
	"time"
)

// KST is Korea Standard Time (UTC+9), the time zone the API uses.
var KST = time.FixedZone("KST", 9*60*60)

// Date returns midnight KST of the given day, for date parameters such as statistics ranges.
func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, KST)
}

// Ptr returns a pointer to v, for optional number and boolean fields:
//
//	bizgo.SendOmniRequest{IdempotencyTTL: bizgo.Ptr(3600), ...}
func Ptr[T any](v T) *T { return &v }

// ChannelMessage is a channel message that can be put in a message flow: [*SMSMessage],
// [*MMSMessage], [*InternationalMessage], [*RCSMessage], [*AlimtalkMessage], [*BrandMessage],
// [*NaverTalkMessage], or a prepared [*MessageFlowItem].
type ChannelMessage interface {
	flowItem() MessageFlowItem
}

// To builds recipients from phone numbers:
//
//	To: bizgo.To("01000000000", "01000001234")
func To(numbers ...string) []Destination {
	out := make([]Destination, len(numbers))
	for i, n := range numbers {
		out[i] = Destination{To: n}
	}
	return out
}

// SendResult is the acceptance result of a send request.
//
// Acceptance is not delivery: the final result arrives later as a report (polling, webhook or
// inquiry). Always check Failed: a request can succeed while some recipients were rejected.
// Every destination is in exactly one of Succeeded, Duplicates and Failed.
type SendResult struct {
	// Destinations are the per-recipient acceptance results, in request order.
	Destinations []SendDestinationResult
	// Ref is the ref you sent with the request.
	Ref string
	// TrackingID is common.infobankTrId, for support inquiries.
	TrackingID string
}

// Per-recipient acceptance codes (destinations[].code).
const (
	destinationAccepted  = "A000" // accepted by this request
	destinationDuplicate = "A301" // accepted earlier by a request with the same idempotency key
)

// Succeeded returns the recipients accepted with code A000 by this request.
func (r *SendResult) Succeeded() []SendDestinationResult {
	var out []SendDestinationResult
	for _, d := range r.Destinations {
		if d.Code == destinationAccepted {
			out = append(out, d)
		}
	}
	return out
}

// Duplicates returns the recipients with code A301: an earlier request with the same idempotency
// key already accepted them, so this request did not send to them again. They are neither
// Succeeded (not newly accepted by this request) nor Failed. Resending with the same key after an
// unknown result (for example a [*ConnectionError]) is therefore safe: recipients accepted the first
// time come back here.
func (r *SendResult) Duplicates() []SendDestinationResult {
	var out []SendDestinationResult
	for _, d := range r.Destinations {
		if d.Code == destinationDuplicate {
			out = append(out, d)
		}
	}
	return out
}

// Failed returns the recipients rejected at acceptance (code other than A000 and A301). They will
// not receive the message. Recipients already accepted with the same idempotency key are in
// Duplicates, not here.
func (r *SendResult) Failed() []SendDestinationResult {
	var out []SendDestinationResult
	for _, d := range r.Destinations {
		if d.Code != destinationAccepted && d.Code != destinationDuplicate {
			out = append(out, d)
		}
	}
	return out
}

// MsgKeys returns the message keys of the recipients in Succeeded, for report and status lookups.
func (r *SendResult) MsgKeys() []string {
	var out []string
	for _, d := range r.Succeeded() {
		if d.MsgKey != "" {
			out = append(out, d.MsgKey)
		}
	}
	return out
}

// ReportBatch is one batch from report polling. Call [ReportsService.Ack] with ReportID after
// you stored it, or use [ReportsService.Consume], which does that for you.
type ReportBatch struct {
	ReportID string
	Reports  []Report
}

// Empty reports whether there was nothing new.
func (b *ReportBatch) Empty() bool { return len(b.Reports) == 0 }

// MessagePage is one page of send history.
type MessagePage struct {
	Messages []MessageStatus
	LastSeq  *int64 // cursor for the next page; nil if the server sent none
	HasNext  bool
}

// MOPage is one page of MO (inbound) history.
type MOPage struct {
	Messages []MOMessage
	LastSeq  *int64
	HasNext  bool
}

// unknownFields returns the members of the JSON object data whose names are not in known.
func unknownFields(data []byte, known []string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

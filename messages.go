package bizgo

import (
	"context"
	"iter"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MessagesService looks up message status, history, statistics and MO (inbound) messages.
// Use it as client.Messages. These APIs are limited to 5 requests per second by default.
type MessagesService struct{ t *transport }

// Service types for statistics and history filters.
const (
	ServiceTypeSMS          = "SMS"
	ServiceTypeMMS          = "MMS"
	ServiceTypeRCS          = "RCS"
	ServiceTypeAlimtalk     = "ALIMTALK"
	ServiceTypeBrandMessage = "BRANDMESSAGE"
)

func (s *MessagesService) statuses(ctx context.Context, op *operation, path string) (*MessageStatusListResponse, error) {
	return call[MessageStatusListResponse](ctx, s.tr(), request{op: op, method: "GET", path: path, policy: retrySafe})
}

// Status returns the acceptance, send and report status of one message. With fallback there is
// one entry per channel tried.
func (s *MessagesService) Status(ctx context.Context, msgKey string) ([]MessageStatus, error) {
	seg, err := segment("msgKey", msgKey)
	if err != nil {
		return nil, err
	}
	resp, err := s.statuses(ctx, opGetMessageStatusByMsgKey, "/api/comm/v1/message/inquiry/msgKey/"+seg)
	if err != nil {
		return nil, err
	}
	return messagesOf(resp), nil
}

// StatusByRequestID returns the status of every message of one broadcast request. requestID is
// a msgKey without its last 3 characters.
func (s *MessagesService) StatusByRequestID(ctx context.Context, requestID string) ([]MessageStatus, error) {
	seg, err := segment("requestId", requestID)
	if err != nil {
		return nil, err
	}
	resp, err := s.statuses(ctx, opGetMessageStatusByRequestID, "/api/comm/v1/message/inquiry/requestId/"+seg)
	if err != nil {
		return nil, err
	}
	return messagesOf(resp), nil
}

func messagesOf(resp *MessageStatusListResponse) []MessageStatus {
	if resp.Data == nil || resp.Data.Data == nil {
		return []MessageStatus{}
	}
	return nonNil(resp.Data.Data.Messages)
}

// StatisticsParams are the parameters of [MessagesService.Statistics].
type StatisticsParams struct {
	// StartDate is required. The date is taken in KST: use [Date] to give a calendar day.
	StartDate time.Time
	// EndDate is optional (zero: not sent).
	EndDate     time.Time
	ServiceType string // ServiceTypeSMS, ... (empty: all)
	GroupKey    string
}

// Statistics returns daily acceptance and report counts.
func (s *MessagesService) Statistics(ctx context.Context, p StatisticsParams) ([]MessageStatistics, error) {
	if p.StartDate.IsZero() {
		return nil, invalid("startDate", "필수 파라미터입니다")
	}
	q := url.Values{}
	q.Set("startDate", yyyymmdd(p.StartDate))
	if !p.EndDate.IsZero() {
		q.Set("endDate", yyyymmdd(p.EndDate))
	}
	setIf(q, "serviceType", p.ServiceType)
	setIf(q, "groupKey", p.GroupKey)
	resp, err := call[MessageStatisticsResponse](ctx, s.tr(), request{
		op: opGetMessageStatistics, method: "GET", path: opGetMessageStatistics.path, query: q, policy: retrySafe,
	})
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || resp.Data.Data == nil {
		return []MessageStatistics{}, nil
	}
	return nonNil(resp.Data.Data.Statistics), nil
}

// HistoryParams are the parameters of [MessagesService.History].
type HistoryParams struct {
	// RequestTime is required. It is sent in KST (yyyy-MM-ddTHH:mm:ss).
	RequestTime  time.Time
	ServiceTypes []string // sent comma separated (empty: all)
	GroupKey     string
	LastSeq      *int64 // cursor from the previous page
	Limit        int    // 1-1000 (0: server default 100)
}

// History returns one page of send history from RequestTime. Use [MessagesService.IterHistory]
// to walk all pages.
func (s *MessagesService) History(ctx context.Context, p HistoryParams) (*MessagePage, error) {
	if p.RequestTime.IsZero() {
		return nil, invalid("requestTime", "필수 파라미터입니다")
	}
	q := url.Values{}
	q.Set("requestTime", p.RequestTime.In(KST).Format("2006-01-02T15:04:05"))
	setIf(q, "serviceType", strings.Join(p.ServiceTypes, ","))
	setIf(q, "groupKey", p.GroupKey)
	if err := pageParams(q, p.LastSeq, p.Limit); err != nil {
		return nil, err
	}
	resp, err := call[MessageStatusListResponse](ctx, s.tr(), request{
		op: opGetMessageHistory, method: "GET", path: opGetMessageHistory.path, query: q, policy: retrySafe,
	})
	if err != nil {
		return nil, err
	}
	page := &MessagePage{Messages: []MessageStatus{}}
	if resp.Data != nil && resp.Data.Data != nil {
		d := resp.Data.Data
		page.Messages, page.LastSeq, page.HasNext = nonNil(d.Messages), d.LastSeq, d.GetHasNext()
	}
	return page, nil
}

// IterHistory iterates over all send history from RequestTime, following lastSeq across pages.
// It stops when hasNext is false, lastSeq is missing or the cursor does not move. A non-nil error
// is yielded once, as the last element.
//
//	for m, err := range client.Messages.IterHistory(ctx, bizgo.HistoryParams{RequestTime: t}) {
//		if err != nil { return err }
//		...
//	}
func (s *MessagesService) IterHistory(ctx context.Context, p HistoryParams) iter.Seq2[MessageStatus, error] {
	return func(yield func(MessageStatus, error) bool) {
		for {
			page, err := s.History(ctx, p)
			if err != nil {
				yield(MessageStatus{}, err)
				return
			}
			for _, m := range page.Messages {
				if !yield(m, nil) {
					return
				}
			}
			if !advance(&p.LastSeq, page.HasNext, page.LastSeq) {
				return
			}
		}
	}
}

// MO returns MO (inbound) messages by key (the same key can occur more than once).
func (s *MessagesService) MO(ctx context.Context, msgKey string) ([]MOMessage, error) {
	seg, err := segment("msgKey", msgKey)
	if err != nil {
		return nil, err
	}
	page, err := s.moPage(ctx, opGetMOByMsgKey, "/api/comm/v1/message/inquiry/mo/msgKey/"+seg, nil)
	if err != nil {
		return nil, err
	}
	return page.Messages, nil
}

// MOHistoryParams are the parameters of [MessagesService.MOHistory].
type MOHistoryParams struct {
	// OccurredTime is required. It is sent in KST with its offset (yyyy-MM-ddTHH:mm:ss+09:00).
	OccurredTime time.Time
	From         string // filter by sender number
	To           string // filter by MO number
	LastSeq      *int64
	Limit        int // 1-1000 (0: server default 100)
}

// MOHistory returns one page of MO history, newest first, from OccurredTime.
func (s *MessagesService) MOHistory(ctx context.Context, p MOHistoryParams) (*MOPage, error) {
	if p.OccurredTime.IsZero() {
		return nil, invalid("occurredTime", "필수 파라미터입니다")
	}
	q := url.Values{}
	q.Set("occurredTime", p.OccurredTime.In(KST).Format("2006-01-02T15:04:05-07:00"))
	setIf(q, "from", p.From)
	setIf(q, "to", p.To)
	if err := pageParams(q, p.LastSeq, p.Limit); err != nil {
		return nil, err
	}
	return s.moPage(ctx, opGetMOHistory, opGetMOHistory.path, q)
}

// IterMOHistory iterates over all MO history pages. See [MessagesService.IterHistory].
func (s *MessagesService) IterMOHistory(ctx context.Context, p MOHistoryParams) iter.Seq2[MOMessage, error] {
	return func(yield func(MOMessage, error) bool) {
		for {
			page, err := s.MOHistory(ctx, p)
			if err != nil {
				yield(MOMessage{}, err)
				return
			}
			for _, m := range page.Messages {
				if !yield(m, nil) {
					return
				}
			}
			if !advance(&p.LastSeq, page.HasNext, page.LastSeq) {
				return
			}
		}
	}
}

func (s *MessagesService) moPage(ctx context.Context, op *operation, path string, q url.Values) (*MOPage, error) {
	resp, err := call[MOMessageListResponse](ctx, s.tr(), request{op: op, method: "GET", path: path, query: q, policy: retrySafe})
	if err != nil {
		return nil, err
	}
	page := &MOPage{Messages: []MOMessage{}}
	if resp.Data != nil && resp.Data.Data != nil {
		d := resp.Data.Data
		page.Messages, page.LastSeq, page.HasNext = nonNil(d.Messages), d.LastSeq, d.GetHasNext()
	}
	return page, nil
}

// advance moves the cursor; false means there is no next page.
func advance[T comparable](cursor **T, hasNext bool, next *T) bool {
	if !hasNext || next == nil || (*cursor != nil && **cursor == *next) {
		return false
	}
	v := *next
	*cursor = &v
	return true
}

func pageParams(q url.Values, lastSeq *int64, limit int) error {
	if limit != 0 {
		if limit < 1 || limit > 1000 {
			return invalid("limit", "limit은 1~1000입니다")
		}
		q.Set("limit", strconv.Itoa(limit))
	}
	if lastSeq != nil {
		q.Set("lastSeq", strconv.FormatInt(*lastSeq, 10))
	}
	return nil
}

func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

func yyyymmdd(t time.Time) string { return t.In(KST).Format("20060102") }

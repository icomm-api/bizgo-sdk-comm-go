package bizgo

import (
	"context"
	"net/url"
)

// ReportsService reads delivery reports. Use it as client.Reports.
//
// Pick one primary method per API key in the console: POLLING (this service) or WEBHOOK (see
// [WebhookReceiver]). Use [ReportsService.Inquiry] to fill gaps.
type ReportsService struct{ t *transport }

// segment escapes one path parameter. Empty, "." and ".." are rejected so that a value can
// never change the path of the request.
func segment(name, value string) (string, error) {
	switch value {
	case "":
		return "", invalid(name, "빈 값은 경로에 쓸 수 없습니다")
	case ".", "..":
		return "", invalid(name, "'.'과 '..'은 경로에 쓸 수 없습니다")
	}
	return url.PathEscape(value), nil
}

// Poll fetches the next batch of reports. The batch is Empty when there is nothing new.
//
// Call [ReportsService.Ack] with the batch's ReportID after storing it; otherwise the same
// reports are returned again. Prefer [ReportsService.Consume], which does this for you.
func (s *ReportsService) Poll(ctx context.Context) (*ReportBatch, error) {
	resp, err := call[ReportPollingResponse](ctx, s.tr(), request{
		op: opGetReportPolling, method: "GET", path: opGetReportPolling.path, policy: retrySafe,
	})
	if err != nil {
		return nil, err
	}
	batch := &ReportBatch{Reports: []Report{}}
	if resp.Data != nil && resp.Data.Data != nil {
		batch.ReportID = resp.Data.Data.ReportID
		batch.Reports = nonNil(resp.Data.Data.Report)
	}
	return batch, nil
}

// Ack confirms receipt of a polled batch.
func (s *ReportsService) Ack(ctx context.Context, reportID string) error {
	seg, err := segment("reportId", reportID)
	if err != nil {
		return err
	}
	_, err = call[APIResponse](ctx, s.tr(), request{
		op: opAckReportPolling, method: "DELETE", path: "/api/comm/v1/report/polling/" + seg, policy: retrySafe,
	})
	return err
}

// ConsumeOptions configure [ReportsService.Consume].
type ConsumeOptions struct {
	// MaxBatches stops after this many batches (0: until no reports are left).
	MaxBatches int
}

// Consume polls until no reports are left, calling handler for each batch and acknowledging a
// batch only after handler returned nil. It returns the number of reports handled.
//
// If handler returns an error, Consume stops and returns it without acknowledging the batch, so
// the batch is delivered again: make handler idempotent (for example upsert by MsgKey).
func (s *ReportsService) Consume(ctx context.Context, handler func(context.Context, []Report) error, opts *ConsumeOptions) (int, error) {
	if handler == nil {
		return 0, invalid("handler", "handler가 nil입니다")
	}
	maxBatches := 0
	if opts != nil {
		maxBatches = opts.MaxBatches
	}
	handled := 0
	for batches := 0; maxBatches <= 0 || batches < maxBatches; batches++ {
		batch, err := s.Poll(ctx)
		if err != nil {
			return handled, err
		}
		if batch.Empty() {
			break
		}
		if err := handler(ctx, batch.Reports); err != nil {
			return handled, err
		}
		if err := s.Ack(ctx, batch.ReportID); err != nil {
			return handled, err
		}
		handled += len(batch.Reports)
	}
	return handled, nil
}

// Inquiry looks up the reports of one message (up to 30 days old).
func (s *ReportsService) Inquiry(ctx context.Context, msgKey string) ([]Report, error) {
	seg, err := segment("msgKey", msgKey)
	if err != nil {
		return nil, err
	}
	resp, err := call[ReportInquiryResponse](ctx, s.tr(), request{
		op: opGetReportInquiry, method: "GET", path: "/api/comm/v1/report/inquiry/" + seg, policy: retrySafe,
	})
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || resp.Data.Data == nil {
		return []Report{}, nil
	}
	return nonNil(resp.Data.Data.Report), nil
}

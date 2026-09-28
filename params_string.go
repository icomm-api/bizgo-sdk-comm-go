package bizgo

import (
	"fmt"
	"log/slog"
)

// String/GoString of the hand-written parameter structs (SDK-DESIGN.md §12.11): phone numbers are
// masked, message text is shown as its length, recipients and messages as their count, so that
// logging parameters with %v, %+v or %#v does not leak them. (The generated <OperationID>Params
// structs get the same methods from tools/gen.)

func (p *printer) count(name string, n int) {
	if n > 0 {
		p.sep(name)
		p.b.WriteString("(" + itoa(n) + "개)")
	}
}

func itoa(n int) string { return formatInt(n) }

// String describes the parameters without phone numbers or text.
func (p SMSParams) String() string {
	pr := newPrinter("SMSParams")
	pr.count("to", len(p.To))
	pr.phone("from", p.From)
	pr.content("text", p.Text)
	pr.value("ref", p.Ref)
	pr.value("idempotencyKey", p.IdempotencyKey)
	return pr.String()
}

// GoString is String.
func (p SMSParams) GoString() string { return p.String() }

// String describes the parameters without phone numbers or text.
func (p LMSParams) String() string {
	pr := newPrinter("LMSParams")
	pr.count("to", len(p.To))
	pr.phone("from", p.From)
	pr.content("text", p.Text)
	pr.content("title", p.Title)
	pr.value("ref", p.Ref)
	pr.value("idempotencyKey", p.IdempotencyKey)
	return pr.String()
}

// GoString is String.
func (p LMSParams) GoString() string { return p.String() }

// String describes the parameters without phone numbers or text.
func (p MMSParams) String() string {
	pr := newPrinter("MMSParams")
	pr.count("to", len(p.To))
	pr.phone("from", p.From)
	pr.content("text", p.Text)
	pr.content("title", p.Title)
	pr.count("fileKeys", len(p.FileKeys))
	pr.value("ref", p.Ref)
	pr.value("idempotencyKey", p.IdempotencyKey)
	return pr.String()
}

// GoString is String.
func (p MMSParams) GoString() string { return p.String() }

// String describes the parameters without recipients or message content.
func (p OmniParams) String() string {
	pr := newPrinter("OmniParams")
	pr.count("to", len(p.To))
	pr.count("messages", len(p.Messages))
	pr.value("ref", p.Ref)
	pr.value("groupKey", p.GroupKey)
	pr.value("paymentCode", p.PaymentCode)
	pr.value("idempotencyKey", p.IdempotencyKey)
	pr.value("idempotencyTtl", p.IdempotencyTTL)
	return pr.String()
}

// GoString is String.
func (p OmniParams) GoString() string { return p.String() }

// String describes the parameters without recipients or message content.
func (p BulkParams) String() string {
	pr := newPrinter("BulkParams")
	pr.count("to", len(p.To))
	pr.count("messages", len(p.Messages))
	pr.value("chunkSize", p.ChunkSize)
	pr.value("concurrency", p.Concurrency)
	pr.value("idempotencyKeyPrefix", p.IdempotencyKeyPrefix)
	pr.value("ref", p.Ref)
	pr.value("groupKey", p.GroupKey)
	pr.value("paymentCode", p.PaymentCode)
	pr.value("idempotencyTtl", p.IdempotencyTTL)
	return pr.String()
}

// GoString is String.
func (p BulkParams) GoString() string { return p.String() }

// String describes the parameters with the phone number filters masked.
func (p MOHistoryParams) String() string {
	pr := newPrinter("MOHistoryParams")
	pr.value("occurredTime", p.OccurredTime)
	pr.phone("from", p.From)
	pr.phone("to", p.To)
	pr.value("lastSeq", p.LastSeq)
	pr.value("limit", p.Limit)
	return pr.String()
}

// GoString is String.
func (p MOHistoryParams) GoString() string { return p.String() }

// String describes the upload without the file content.
func (p UploadParams) String() string {
	pr := newPrinter("UploadParams")
	pr.file("file", p.File != nil)
	pr.value("filename", p.Filename)
	pr.value("fileKey", p.FileKey)
	pr.value("imageName", p.ImageName)
	return pr.String()
}

// GoString is String.
func (p UploadParams) GoString() string { return p.String() }

// GoString is String, so that %#v does not print the reader.
func (u UploadFile) GoString() string { return u.String() }

// LogValue methods: log/slog (also its JSON handler) logs the masked String form.

// LogValue implements slog.LogValuer.
func (p SMSParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p LMSParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p MMSParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p OmniParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p BulkParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p MOHistoryParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (p UploadParams) LogValue() slog.Value { return slog.StringValue(p.String()) }

// LogValue implements slog.LogValuer.
func (u UploadFile) LogValue() slog.Value { return slog.StringValue(u.String()) }

// LogValue logs the result with masked recipients (the JSON handler would print them raw otherwise).
func (r *SendResult) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%+v", *r)) }

// LogValue logs the result with masked recipients.
func (r *BulkSendResult) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%+v", *r)) }

// LogValue logs the batch with masked reports.
func (b *ReportBatch) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%+v", *b)) }

// LogValue logs the page with masked messages.
func (p *MessagePage) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%+v", *p)) }

// LogValue logs the page with masked messages.
func (p *MOPage) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%+v", *p)) }

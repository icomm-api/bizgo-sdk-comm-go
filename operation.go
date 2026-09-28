package bizgo

// Rate limit buckets (SDK-DESIGN.md §11.2, §11.5).
const (
	bucketSend  = "send"  // operations marked x-sdk-rate: send; cost = recipients
	bucketOther = "other" // everything else; cost = 1 request
)

// operation is the metadata of one spec operation, generated into services_gen.go.
type operation struct {
	id          string // operationId
	name        string // <x-sdk-resource>.<x-sdk-method>
	method      string
	path        string // path template, for example /api/comm/v1/report/inquiry/{msgKey}
	retry       retryPolicy
	rate        string // bucketSend or bucketOther
	pagination  string // cursor, page, offset or ""
	handwritten bool
}

// OperationInfo describes one operation of the Bizgo API as the SDK implements it. It is what
// [Hooks] and the bizgotest package use to identify a call. It holds no request values.
type OperationInfo struct {
	// ID is the operationId of the spec, for example "listAlimtalkTemplates".
	ID string
	// Name is "<x-sdk-resource>.<x-sdk-method>", for example "alimtalk.templates.list": the method is
	// client.Alimtalk.Templates.List.
	Name string
	// Method is the HTTP method.
	Method string
	// PathTemplate is the path with {placeholders}, never real values.
	PathTemplate string
	// Retry is "safe" (429, 5xx and network errors are retried) or "rate_limit_only" (429 only).
	// Send.Omni and friends retry like "safe" when you give an idempotency key.
	Retry string
	// RateBucket is "send" (cost: the number of recipients) or "other" (cost: 1 request).
	RateBucket string
	// Pagination is "cursor", "page", "offset" or "" (no Iter method).
	Pagination string
}

func (op *operation) info() OperationInfo {
	retry := "safe"
	if op.retry == retryRateLimitOnly {
		retry = "rate_limit_only"
	}
	return OperationInfo{ID: op.id, Name: op.name, Method: op.method, PathTemplate: op.path, Retry: retry,
		RateBucket: op.rate, Pagination: op.pagination}
}

// Operations returns every operation of the spec, in spec order.
func Operations() []OperationInfo {
	out := make([]OperationInfo, len(operationTable))
	for i, op := range operationTable {
		out[i] = op.info()
	}
	return out
}

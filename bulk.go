package bizgo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Bulk sending (SDK-DESIGN.md §11.1, §12.2, §12.3).

const (
	// MaxChunkSize is the largest number of recipients of one send request.
	MaxChunkSize = 200
	// DefaultBulkConcurrency is the default number of chunk requests in flight.
	DefaultBulkConcurrency = 4
	maxIdempotencyKey      = 200
)

// ErrChunkPanic is the kind of a *PanicError: a bulk chunk whose send panicked (or called
// runtime.Goexit). The other chunks and the results already received are kept.
var ErrChunkPanic = errors.New("bizgo: 청크를 보내는 중 panic이 발생했습니다")

// PanicError reports a panic (or runtime.Goexit) during a call: the error of a bulk chunk, and the
// error hooks see when a call ends by a panic. It keeps the panic value's type only, never the
// value, which could hold request data. It matches ErrChunkPanic.
type PanicError struct {
	Type   string // type of the panic value, for example "string" or "runtime.Error"
	Goexit bool   // the goroutine called runtime.Goexit instead of panicking
}

func (e *PanicError) Error() string {
	if e.Goexit {
		return "bizgo: 청크를 보내는 중 runtime.Goexit가 호출되었습니다"
	}
	return "bizgo: 청크를 보내는 중 panic이 발생했습니다(" + e.Type + ")"
}

// Is matches ErrChunkPanic.
func (e *PanicError) Is(target error) bool { return target == ErrChunkPanic }

func panicType(p any) string {
	switch p.(type) {
	case nil:
		return "runtime.Goexit"
	case runtime.Error:
		return "runtime.Error"
	case error:
		return "error"
	}
	return fmt.Sprintf("%T", p)
}

// BulkParams are the parameters of [SendService.Bulk].
type BulkParams struct {
	// To are the recipients. There is no limit: they are sent ChunkSize at a time.
	To []Destination
	// Messages are the channel messages in fallback order, as for [SendService.Omni].
	Messages []ChannelMessage
	// ChunkSize is the number of recipients per request, 1-200 (0: 200).
	ChunkSize int
	// Concurrency is the number of requests in flight at once (0: 4). The client's rate limit (send
	// bucket, 200 messages per second by default) applies as well, so 200-recipient chunks go out
	// about one per second.
	Concurrency int
	// IdempotencyKeyPrefix, when set, gives every chunk the idempotency key
	// "<prefix>-<chunkSize>-<start index>-<hash8>" (hash8: the first 8 hex digits of SHA-256 over the
	// chunk's phone numbers joined by "\n"). Run the same list again with the same ChunkSize (within
	// IdempotencyTTL) and the recipients of chunks already accepted come back in
	// [BulkSendResult.Duplicates] (A301) instead of being delivered twice; a key is never reused for other recipients. To resend only the failed chunks,
	// use the chunk numbers in [BulkSendResult.Errors]. With a prefix, timeouts and 5xx are retried;
	// without one, chunks are retried only on 429. Every key must fit in 200 characters.
	IdempotencyKeyPrefix string
	// Ref, GroupKey, PaymentCode and IdempotencyTTL are sent with every chunk, as for [SendService.Omni].
	// With IdempotencyKeyPrefix and a nil IdempotencyTTL, every chunk gets [DefaultIdempotencyTTL].
	Ref            string
	GroupKey       string
	PaymentCode    string
	IdempotencyTTL *int
}

// BulkChunkError is the failure of one chunk. It names the chunk and the index range of its
// recipients in BulkParams.To, never the phone numbers.
type BulkChunkError struct {
	Chunk int // chunk number, from 0
	Start int // index of the first recipient of the chunk
	End   int // index after the last recipient
	Err   error
}

func (e BulkChunkError) Error() string {
	return "청크 " + strconv.Itoa(e.Chunk) + "(수신자 " + strconv.Itoa(e.Start) + "~" + strconv.Itoa(e.End-1) + "): " + e.Err.Error()
}

// Unwrap returns the error of the chunk.
func (e BulkChunkError) Unwrap() error { return e.Err }

// BulkSendResult is the result of [SendService.Bulk].
type BulkSendResult struct {
	// Results has one entry per chunk, in order: nil for a chunk that failed (see Errors).
	Results []*SendResult
	// Errors lists the failed chunks, in order.
	Errors []BulkChunkError
}

// Succeeded returns the recipients accepted with code A000, over all chunks.
func (r *BulkSendResult) Succeeded() []SendDestinationResult {
	var out []SendDestinationResult
	for _, c := range r.Results {
		if c != nil {
			out = append(out, c.Succeeded()...)
		}
	}
	return out
}

// Duplicates returns the recipients already accepted by an earlier request with the same
// idempotency key (code A301), over all chunks. A chunk whose recipients are all duplicates is not an
// error: rerunning the same list with the same IdempotencyKeyPrefix and ChunkSize returns the
// chunks accepted the first time here.
func (r *BulkSendResult) Duplicates() []SendDestinationResult {
	var out []SendDestinationResult
	for _, c := range r.Results {
		if c != nil {
			out = append(out, c.Duplicates()...)
		}
	}
	return out
}

// Failed returns the recipients rejected at acceptance, over all chunks (not the recipients of
// failed chunks: see Errors; not the duplicates: see Duplicates).
func (r *BulkSendResult) Failed() []SendDestinationResult {
	var out []SendDestinationResult
	for _, c := range r.Results {
		if c != nil {
			out = append(out, c.Failed()...)
		}
	}
	return out
}

// MsgKeys returns the message keys of the accepted recipients, over all chunks.
func (r *BulkSendResult) MsgKeys() []string {
	var out []string
	for _, c := range r.Results {
		if c != nil {
			out = append(out, c.MsgKeys()...)
		}
	}
	return out
}

// BulkIdempotencyKey returns "<prefix>-<chunkSize>-<start>-<hash8>", the idempotency key
// [SendService.Bulk] gives a chunk. hash8 is the first 8 hex digits of SHA-256 over the chunk's phone
// numbers, in order, joined by "\n" (UTF-8).
func BulkIdempotencyKey(prefix string, chunkSize, start int, chunk []Destination) string {
	numbers := make([]string, len(chunk))
	for i, d := range chunk {
		numbers[i] = d.To
	}
	sum := sha256.Sum256([]byte(strings.Join(numbers, "\n")))
	return prefix + "-" + strconv.Itoa(chunkSize) + "-" + strconv.Itoa(start) + "-" + hex.EncodeToString(sum[:4])
}

type bulkChunk struct {
	index, start, end int
	req               *SendOmniRequest
}

// Bulk sends one message to any number of recipients, ChunkSize (up to 200) at a time, with
// [SendService.Request] per chunk.
//
// Every chunk is validated before the first one is sent: if one is invalid nothing is sent and a
// [*ValidationError] is returned (recipient indexes refer to p.To). After that Bulk never fails as a
// whole: a chunk that fails for any reason (API or network error, unreadable response, panic,
// canceled context) is reported in Errors and does not stop the others or lose the results already
// received. Check Errors and Failed (recipients accepted by an earlier run with the same keys are in
// Duplicates).
func (s *SendService) Bulk(ctx context.Context, p BulkParams) (*BulkSendResult, error) {
	if s.tr() == nil {
		return nil, errZeroClient
	}
	if ctx == nil {
		return nil, invalid("ctx", "context가 nil입니다. context.Background() 등을 넘기세요")
	}
	chunks, err := s.bulkChunks(p)
	if err != nil {
		return nil, err
	}
	concurrency := p.Concurrency
	if concurrency == 0 {
		concurrency = DefaultBulkConcurrency
	}
	result := &BulkSendResult{Results: make([]*SendResult, len(chunks))}
	errs := make([]error, len(chunks))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, c := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			completed := false
			defer func() {
				if !completed { // runtime.Goexit (for example t.FailNow in a test RoundTripper)
					result.Results[c.index], errs[c.index] = nil, &PanicError{Type: "runtime.Goexit", Goexit: true}
				}
				<-sem
				wg.Done()
			}()
			result.Results[c.index], errs[c.index] = s.sendChunk(ctx, c)
			completed = true
		}()
	}
	wg.Wait()
	for _, c := range chunks {
		if errs[c.index] != nil {
			result.Results[c.index] = nil
			result.Errors = append(result.Errors, BulkChunkError{Chunk: c.index, Start: c.start, End: c.end, Err: errs[c.index]})
		}
	}
	return result, nil
}

// sendChunk sends one chunk; a panic becomes a *PanicError (the panic's type only, never its value).
func (s *SendService) sendChunk(ctx context.Context, c bulkChunk) (res *SendResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, &PanicError{Type: panicType(p)}
		}
	}()
	if ctx != nil && ctx.Err() != nil {
		return nil, connectionError(ctx.Err(), ctx.Err()) // not sent
	}
	return s.Request(ctx, c.req)
}

func (s *SendService) bulkChunks(p BulkParams) ([]bulkChunk, error) {
	size := p.ChunkSize
	if size == 0 {
		size = MaxChunkSize
	}
	if size < 1 || size > MaxChunkSize {
		return nil, invalid("chunkSize", "chunkSize는 1~200입니다")
	}
	if p.Concurrency < 0 {
		return nil, invalid("concurrency", "concurrency는 1 이상입니다")
	}
	if len(p.To) == 0 {
		return nil, invalid("destinations", "수신자가 없습니다")
	}
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
	var chunks []bulkChunk
	var problems []FieldProblem
	seen := map[string]bool{}
	for index, start := 0, 0; start < len(p.To); index, start = index+1, start+size {
		end := min(start+size, len(p.To))
		req := &SendOmniRequest{
			Destinations: p.To[start:end], MessageFlow: flow, Ref: p.Ref, GroupKey: p.GroupKey,
			PaymentCode: p.PaymentCode, IdempotencyTTL: p.IdempotencyTTL,
		}
		if p.IdempotencyKeyPrefix != "" {
			req.IdempotencyKey = BulkIdempotencyKey(p.IdempotencyKeyPrefix, size, start, p.To[start:end])
			if len(req.IdempotencyKey) > maxIdempotencyKey {
				return nil, invalid("idempotencyKeyPrefix", fmt.Sprintf("idempotencyKeyPrefix가 너무 깁니다(생성된 키가 %d자를 넘음)", maxIdempotencyKey))
			}
		}
		if err := req.Validate(); err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				for _, pr := range ve.Problems {
					pr.Path = shiftDestinationIndex(pr.Path, start)
					if !seen[pr.Path+pr.Reason] {
						seen[pr.Path+pr.Reason] = true
						problems = append(problems, pr)
					}
				}
			}
			continue
		}
		chunks = append(chunks, bulkChunk{index: index, start: start, end: end, req: req})
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return chunks, nil
}

// shiftDestinationIndex turns "destinations[3].to" of a chunk into the index in the whole list.
func shiftDestinationIndex(path string, start int) string {
	rest, ok := strings.CutPrefix(path, "destinations[")
	if !ok {
		return path
	}
	i := strings.IndexByte(rest, ']')
	n, err := strconv.Atoi(rest[:max(i, 0)])
	if i < 0 || err != nil {
		return path
	}
	return "destinations[" + strconv.Itoa(n+start) + rest[i:]
}

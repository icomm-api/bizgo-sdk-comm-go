package bizgo

import (
	"math"
	"sync"
	"time"
)

// Client-side rate limiting (SDK-DESIGN.md §11.2, §11.5, §12.14).
//
// Every client has two token buckets:
//
//   - send: default 200 messages per second, counted per recipient. A request of an operation
//     marked x-sdk-rate: send in the spec costs len(destinations) tokens (minimum 1; sends without a
//     recipient list, such as counsel talk messages or a brand message group send to a friend group,
//     cost 1). The operations are sendOmni (Send.Omni, SMS, LMS, MMS, Request, Bulk),
//     createReservation, addReservationRecipients, createBrandMessageGroupSend, sendCounselPlain and
//     sendCounselRich; see [Operations] (RateBucket "send").
//   - other: default 5 requests per second for every other operation.
//
// A bucket holds up to one second of tokens and starts full, like the server's token bucket. Every
// HTTP attempt, retries included, waits for its tokens. A request that costs more than the capacity
// waits until the bucket is full and leaves the balance negative (debt), so it never blocks forever
// and the average rate still holds.
//
// The Bizgo limit is per account: these buckets pace one Client in one process only. Several
// processes or servers that share a key need their own coordination; HTTP 429 is still retried.

// Default rates.
const (
	DefaultSendRate  = 200.0 // messages (recipients) per second
	DefaultOtherRate = 5.0   // requests per second
)

type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	capacity float64
	tokens   float64
	updated  time.Time
	now      func() time.Time
}

func newTokenBucket(rate float64, now func() time.Time) *tokenBucket {
	return &tokenBucket{rate: rate, capacity: rate, tokens: rate, updated: now(), now: now}
}

// reserve takes cost tokens and returns how long the caller must wait before sending. The caller may
// go when the balance reaches min(cost, capacity); the rest stays as debt.
func (b *tokenBucket) reserve(cost int) time.Duration {
	c := math.Max(float64(cost), 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.rate)
	}
	b.updated = now
	need := math.Min(c, b.capacity)
	var wait time.Duration
	if b.tokens < need {
		wait = time.Duration((need - b.tokens) / b.rate * float64(time.Second))
	}
	b.tokens -= c
	return wait
}

type rateLimiter struct {
	send, other *tokenBucket
}

func newRateLimiter(send, other float64, now func() time.Time) *rateLimiter {
	return &rateLimiter{send: newTokenBucket(send, now), other: newTokenBucket(other, now)}
}

func (l *rateLimiter) reserve(bucket string, cost int) time.Duration {
	if bucket == bucketSend {
		return l.send.reserve(cost)
	}
	return l.other.reserve(1)
}

func validRate(r float64) bool { return r > 0 && !math.IsInf(r, 0) && !math.IsNaN(r) }

// Package bizgo is the Go SDK for the Bizgo Communication API: SMS/LMS/MMS, international SMS,
// RCS, Kakao AlimTalk, BrandMessage and Counsel talk, and Naver TalkTalk through one client, plus
// delivery reports, message history and statistics, webhook verification, and every other
// operation of the API (reservations, templates, sender profiles, insights, ...) as generated
// resource methods: client.Alimtalk.Templates.List, client.Reservations.Create, ... (see [Operations]).
//
// Convenience features: [SendService.Bulk] for any number of recipients, a client-side rate limit
// ([WithRateLimit]), observability [Hooks] (an OpenTelemetry adapter is the separate module
// github.com/icomm-api/bizgo-sdk-comm-go/otel), and the bizgotest package for offline tests.
//
// Quick start:
//
//	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox)) // API key from BIZGO_API_KEY
//	if err != nil { ... }
//
//	// AlimTalk, falling back to SMS if it fails
//	result, err := client.Send.Omni(ctx, bizgo.OmniParams{
//		To: bizgo.To("01000000000"),
//		Messages: []bizgo.ChannelMessage{
//			&bizgo.AlimtalkMessage{SenderKey: "SENDER_KEY_EXAMPLE", TemplateCode: "TEMPLATE_CODE_EXAMPLE", MsgType: "AT", Text: "..."},
//			&bizgo.SMSMessage{From: "01000000000", Text: "..."},
//		},
//		IdempotencyKey: "order-1234",
//	})
//
// With an IdempotencyKey and no IdempotencyTTL, [DefaultIdempotencyTTL] (86400 seconds) is sent,
// because Bizgo rejects a key without a TTL (A309); an explicit TTL, 0 included, is kept. A resend
// with the same key is not delivered twice: recipients accepted earlier come back in
// [SendResult.Duplicates] (per-recipient A301), neither in Failed nor in Succeeded.
//
// Requests are validated before sending ([*ValidationError]). Failures from Bizgo are
// [*APIError] values; test them with errors.Is (ErrAuthentication, ErrRateLimit,
// ErrDuplicateRequest, ...). Nothing the SDK logs or returns in an error message contains the
// API key, request bodies, query strings or phone numbers.
//
// Models are generated from the OpenAPI spec in spec/openapi.yaml (see tools/gen). Optional
// string fields are omitted when empty; optional numbers and booleans are pointers (use [Ptr]).
// Response models keep fields that are not in the spec in Extra.
//
// API reference: https://developers.bizgo.io/api-sdk/api-reference
package bizgo

//go:generate go -C tools/gen run . -root ../..

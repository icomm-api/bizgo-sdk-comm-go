package bizgo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

const sendRoute = "/api/comm/v1/send/omni"

func TestSMSRequestBodyAndHeaders(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	result, err := c.Send.SMS(context.Background(), SMSParams{To: To(phone), From: phone, Text: "인증번호는 123456 입니다.", Ref: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	call := route.Last(t)
	if got := call.Header.Get("Authorization"); got != apiKey { // raw key, no prefix (verified on sandbox)
		t.Fatalf("Authorization = %q", got)
	}
	if !strings.HasPrefix(call.Header.Get("User-Agent"), "bizgo-sdk-comm-go/"+Version+" go/") {
		t.Fatalf("User-Agent = %q", call.Header.Get("User-Agent"))
	}
	if call.Header.Get("Accept") != "application/json" || call.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", call.Header)
	}
	jsonEqual(t, call.Body, `{
		"destinations": [{"to": "01000000000"}],
		"messageFlow": [{"sms": {"from": "01000000000", "text": "인증번호는 123456 입니다."}}],
		"ref": "ref-1"
	}`)
	if got := result.MsgKeys(); len(got) != 1 || got[0] != "KEY000" {
		t.Fatalf("MsgKeys = %v", got)
	}
	if len(result.Failed()) != 0 || result.Ref != "ref-1" || result.TrackingID != "TR-TEST" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFallbackFlowKeepsOrderAndOmitsUnsetDefaults(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	_, err := c.Send.Omni(context.Background(), OmniParams{
		To: []Destination{{To: phone, ReplaceWords: map[string]string{"name": "홍길동"}}},
		Messages: []ChannelMessage{
			&AlimtalkMessage{SenderKey: "SENDER_KEY_EXAMPLE", TemplateCode: "TEMPLATE_CODE_EXAMPLE", MsgType: "AT", Text: "#{name}님 주문 완료"},
			&SMSMessage{From: phone, Text: "#{name}님 주문 완료"},
		},
		IdempotencyKey: "order-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// responseMethod / timeout defaults of the spec are not sent
	jsonEqual(t, route.Last(t).Body, `{
		"destinations": [{"to": "01000000000", "replaceWords": {"name": "홍길동"}}],
		"messageFlow": [
			{"alimtalk": {"senderKey": "SENDER_KEY_EXAMPLE", "templateCode": "TEMPLATE_CODE_EXAMPLE", "msgType": "AT", "text": "#{name}님 주문 완료"}},
			{"sms": {"from": "01000000000", "text": "#{name}님 주문 완료"}}
		],
		"idempotencyKey": "order-1",
		"idempotencyTtl": 86400
	}`)
}

func TestZeroValuesAreSentWhenSet(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	_, err := c.Send.Omni(context.Background(), OmniParams{
		To:             To(phone),
		Messages:       []ChannelMessage{&SMSMessage{From: phone, Text: "x"}},
		IdempotencyKey: "k",
		IdempotencyTTL: Ptr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := decodeBody(t, route.Last(t).Body)["idempotencyTtl"]; !ok || v != float64(0) {
		t.Fatalf("idempotencyTtl = %v (%v)", v, ok)
	}
}

func TestPartialFailureIsReportedPerRecipient(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("POST", sendRoute, accepted("A000", "A306"))
	result, err := c.Send.SMS(context.Background(), sms(phone, otherPhone))
	if err != nil {
		t.Fatal(err)
	}
	if f := result.Failed(); len(f) != 1 || f[0].Code != "A306" || len(result.Succeeded()) != 1 {
		t.Fatalf("failed = %+v", f)
	}
}

// §12.4: a resend with the same idempotencyKey succeeds (HTTP 200, data.code A000) with A301 per
// recipient. Those recipients are Duplicates: neither Failed nor Succeeded.
func TestPerRecipientA301IsDuplicateNotFailed(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("POST", sendRoute, accepted("A301", "A301"), accepted("A000", "A301", "A306"))
	p := sms(phone, otherPhone)
	p.IdempotencyKey = "k-1"
	result, err := c.Send.SMS(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Duplicates()) != 2 || len(result.Failed()) != 0 || len(result.Succeeded()) != 0 || len(result.MsgKeys()) != 0 {
		t.Fatalf("duplicates %d, failed %d, succeeded %d", len(result.Duplicates()), len(result.Failed()), len(result.Succeeded()))
	}
	for _, text := range []string{fmt.Sprint(result.Duplicates()), fmt.Sprintf("%+v", *result), fmt.Sprintf("%#v", result.Duplicates()), result.LogValue().String()} {
		if strings.Contains(text, phone) || !strings.Contains(text, "010****0000") {
			t.Fatalf("not masked: %s", text)
		}
	}

	// mixed response: one of each
	p.To = To(phone, otherPhone, phone)
	result, err = c.Send.SMS(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s, d, f := result.Succeeded(), result.Duplicates(), result.Failed()
	if len(s) != 1 || s[0].Code != "A000" || len(d) != 1 || d[0].Code != "A301" || len(f) != 1 || f[0].Code != "A306" ||
		len(result.MsgKeys()) != 1 || result.MsgKeys()[0] != s[0].MsgKey {
		t.Fatalf("succeeded %+v, duplicates %+v, failed %+v", s, d, f)
	}
}

func TestMMSAndLMS(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	ctx := context.Background()
	if _, err := c.Send.MMS(ctx, MMSParams{To: To(phone), From: phone, Text: "본문", Title: "제목", FileKeys: []string{"FILE_KEY_001"}}); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, route.Last(t).Body, `{"destinations":[{"to":"01000000000"}],
		"messageFlow":[{"mms":{"from":"01000000000","title":"제목","text":"본문","fileKey":["FILE_KEY_001"]}}]}`)
	if _, err := c.Send.LMS(ctx, LMSParams{To: To(phone), From: phone, Text: "본문"}); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, route.Last(t).Body, `{"destinations":[{"to":"01000000000"}],"messageFlow":[{"mms":{"from":"01000000000","text":"본문"}}]}`)
	if _, err := c.Send.MMS(ctx, MMSParams{To: To(phone), From: phone, Text: "본문"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("MMS without file keys: %v", err)
	}
}

func TestRCSButtonsAreSerialized(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	_, err := c.Send.Omni(context.Background(), OmniParams{
		To: To(phone),
		Messages: []ChannelMessage{&RCSMessage{
			From: phone, FormatID: "FORMAT_ID_EXAMPLE", BrandKey: "BRAND_KEY_EXAMPLE",
			Body: &RCSBody{Title: "t", Description: "d"},
			Buttons: []RCSButton{{Suggestions: []RCSSuggestion{{
				DisplayText: "열기",
				Action:      &RCSAction{URLAction: &RCSURLAction{OpenURL: &RCSURLActionOpenURL{URL: "https://example.com"}}},
			}}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, route.Last(t).Body, `{"destinations":[{"to":"01000000000"}],"messageFlow":[{"rcs":{
		"from":"01000000000","formatId":"FORMAT_ID_EXAMPLE","brandKey":"BRAND_KEY_EXAMPLE",
		"body":{"title":"t","description":"d"},
		"buttons":[{"suggestions":[{"action":{"urlAction":{"openUrl":{"url":"https://example.com"}}},"displayText":"열기"}]}]}}]}`)
}

func TestSMSByteLimitIsCheckedBeforeSending(t *testing.T) {
	for _, tc := range []struct{ text, reason string }{
		{strings.Repeat("가", 46), "최대 90byte"},
		{"안녕😀", "EUC-KR"},
		{"✅ 완료", "EUC-KR"}, // BMP symbol that CP949 cannot encode
	} {
		c, srv, _ := newTestClient(t)
		route := srv.On("POST", sendRoute, accepted("A000"))
		_, err := c.Send.SMS(context.Background(), SMSParams{To: To(phone), From: phone, Text: tc.text})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Problems[0].Path != "messageFlow[0].sms.text" {
			t.Fatalf("%q: err = %v", tc.text, err)
		}
		mustContain(t, err.Error(), tc.reason)
		if route.Count() != 0 {
			t.Fatal("request was sent")
		}
	}
}

func TestSMSLimitCountsBytesNotCharacters(t *testing.T) {
	check := func(m ChannelMessage) error {
		return (&SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{m.flowItem()}}).Validate()
	}
	for _, ok := range []ChannelMessage{
		&SMSMessage{From: phone, Text: strings.Repeat("가", 45)}, // 90 bytes
		&SMSMessage{From: phone, Text: strings.Repeat("a", 90)},
		&MMSMessage{From: phone, Text: strings.Repeat("가", 1000)}, // 2,000 bytes
		&SMSMessage{From: phone, Text: "漢字 ①②"},                   // hanja and KS X 1001 symbols
	} {
		if err := check(ok); err != nil {
			t.Fatalf("%+v: %v", ok, err)
		}
	}
	if err := check(&MMSMessage{From: phone, Text: strings.Repeat("가", 1001)}); !errors.Is(err, ErrValidation) {
		t.Fatalf("2,002 bytes accepted: %v", err)
	}
	if n, err := EUCKRLen("똠방각하 abc"); err != nil || n != 12 { // 똠 is outside KS X 1001 but in CP949
		t.Fatalf("EUCKRLen = %d, %v", n, err)
	}
	if _, err := EUCKRLen("\xff"); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestMaxLengthCountsCodePoints(t *testing.T) {
	msg := &AlimtalkMessage{SenderKey: "S", TemplateCode: "T", MsgType: "AT", Text: "x", Title: strings.Repeat("가", 50)}
	req := &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{Alimtalk: msg}}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.Title += "a"
	err := req.Validate()
	mustContain(t, fmt.Sprint(err), "messageFlow[0].alimtalk.title: 최대 50자인데 51자입니다")
}

func TestRequiredFieldsAreReportedTogether(t *testing.T) {
	err := (&SendOmniRequest{MessageFlow: []MessageFlowItem{{SMS: &SMSMessage{}}}}).Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range ve.Problems {
		paths = append(paths, p.Path)
	}
	want := "destinations messageFlow[0].sms.from messageFlow[0].sms.text"
	if strings.Join(paths, " ") != want {
		t.Fatalf("paths = %v", paths)
	}
}

func TestEnumsPatternsAndRangesAreChecked(t *testing.T) {
	cases := map[string]*SendOmniRequest{
		"messageFlow[0].alimtalk.msgType: 허용값은 AT, AI 중 하나입니다": {
			Destinations: To(phone), MessageFlow: []MessageFlowItem{{Alimtalk: &AlimtalkMessage{SenderKey: "S", TemplateCode: "T", MsgType: "XX"}}}},
		"messageFlow[0].sms.ttl: 형식이 맞지 않습니다": {
			Destinations: To(phone), MessageFlow: []MessageFlowItem{{SMS: &SMSMessage{From: phone, Text: "x", TTL: "1h"}}}},
		"idempotencyTtl: 최댓값은 86400입니다": {
			Destinations: To(phone), MessageFlow: []MessageFlowItem{{SMS: &SMSMessage{From: phone, Text: "x"}}}, IdempotencyTTL: Ptr(86401)},
		"messageFlow[0].mms.fileKey: 최대 3개인데 4개입니다": {
			Destinations: To(phone), MessageFlow: []MessageFlowItem{{MMS: &MMSMessage{From: phone, Text: "x", FileKey: []string{"a", "b", "c", "d"}}}}},
		"messageFlow[0].brandmessage.attachment.commerce.regularPrice: 최댓값은 99999999입니다": {
			Destinations: To(phone), MessageFlow: []MessageFlowItem{{BrandMessage: &BrandMessage{SendType: "basic", SenderKey: "S",
				Attachment: &BrandMessageAttachment{Commerce: &BrandMessageCommerce{Title: "t", RegularPrice: 100000000}}}}}},
	}
	for want, req := range cases {
		mustContain(t, fmt.Sprint(req.Validate()), want)
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	_, err := ParseSendOmniRequest([]byte(`{"destinations":[{"to":"01000000000"}],
		"messageFlow":[{"alimtalk":{"senderKey":"S","templateCode":"T","templatecode":"typo"}}]}`))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err.Error(), "messageFlow[0].alimtalk.templatecode: 알 수 없는 필드입니다")
	_, err = ParseSendOmniRequest([]byte(`{"destinations":[{"to":"01000000000","extra":1}],"messageFlow":[{"sms":{"from":"1","text":"x"}}]}`))
	mustContain(t, fmt.Sprint(err), "destinations[0].extra: 알 수 없는 필드입니다")
}

func TestRequestFromJSONIsSentAsIs(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	raw := `{"destinations":[{"to":"01000000000"}],"messageFlow":[{"sms":{"from":"01000000000","text":"x","ttl":"60"}}],"groupKey":"g"}`
	req, err := ParseSendOmniRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send.Request(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, route.Last(t).Body, raw)
}

func TestMoreThan200RecipientsAreRejected(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	numbers := make([]string, 201)
	for i := range numbers {
		numbers[i] = fmt.Sprintf("010%08d", i)
	}
	_, err := c.Send.SMS(context.Background(), sms(numbers...))
	mustContain(t, fmt.Sprint(err), "destinations: 최대 200개인데 201개입니다")
	if route.Count() != 0 {
		t.Fatal("request was sent")
	}
}

func TestMessageFlowItemNeedsExactlyOneChannel(t *testing.T) {
	c, _, _ := newTestClient(t)
	_, err := c.Send.Omni(context.Background(), OmniParams{
		To:       To(phone),
		Messages: []ChannelMessage{&MessageFlowItem{SMS: &SMSMessage{From: "1", Text: "x"}, MMS: &MMSMessage{From: "1", Text: "x"}}},
	})
	mustContain(t, fmt.Sprint(err), "messageFlow[0]: 채널 키(sms, mms, international, rcs, alimtalk, brandmessage, navertalk) 중 정확히 하나가 있어야 합니다")
	_, err = c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: []ChannelMessage{&MessageFlowItem{}}})
	mustContain(t, fmt.Sprint(err), "정확히 하나")
	_, err = ParseSendOmniRequest([]byte(`{"destinations":[{"to":"1"}],"messageFlow":[{"sms":{"from":"1","text":"x"},"mms":{"from":"1","text":"x"}}]}`))
	mustContain(t, fmt.Sprint(err), "정확히 하나")
}

func TestValidationErrorsDoNotEchoInput(t *testing.T) {
	c, _, _ := newTestClient(t)
	_, err := c.Send.SMS(context.Background(), SMSParams{To: To(otherPhone), From: otherPhone, Text: "😀" + otherPhone})
	if !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	mustNotContain(t, err.Error(), otherPhone, "😀")
	mustNotContain(t, fmt.Sprintf("%+v %#v", err, err), otherPhone)
}

func TestEmptyMessagesAreRejected(t *testing.T) {
	c, _, _ := newTestClient(t)
	_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone)})
	mustContain(t, fmt.Sprint(err), "메시지가 없습니다")
	_, err = c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: []ChannelMessage{nil}})
	mustContain(t, fmt.Sprint(err), "messageFlow[0]")
}

func TestSendWithoutIdempotencyKeyIsNotRetriedAfterTimeout(t *testing.T) {
	c, srv, _ := newTestClient(t, WithTimeout(100*time.Millisecond))
	route := srv.On("POST", sendRoute, ts.Reply{Hang: true})
	_, err := c.Send.SMS(context.Background(), sms(phone))
	var ce *ConnectionError
	if !errors.As(err, &ce) || !ce.Timeout || !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %#v", err)
	}
	if route.Count() != 1 {
		t.Fatalf("calls = %d", route.Count())
	}
}

func TestSendWithIdempotencyKeyIsRetriedAfterTimeout(t *testing.T) {
	c, srv, d := newTestClient(t, WithTimeout(100*time.Millisecond))
	route := srv.On("POST", sendRoute, ts.Reply{Hang: true}, accepted("A000"))
	p := sms(phone)
	p.IdempotencyKey = "k-1"
	result, err := c.Send.SMS(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if route.Count() != 2 || len(result.MsgKeys()) != 1 || len(d.all()) != 1 {
		t.Fatalf("calls = %d, delays = %v", route.Count(), d.all())
	}
}

func TestSendWithoutIdempotencyKeyIsNotRetriedOnServerOrConnectionErrors(t *testing.T) {
	for _, reply := range []ts.Reply{ts.Text(503, "busy"), {Disconnect: true}} {
		c, srv, _ := newTestClient(t)
		route := srv.On("POST", sendRoute, reply, accepted("A000"))
		if _, err := c.Send.SMS(context.Background(), sms(phone)); err == nil {
			t.Fatal("no error")
		}
		if route.Count() != 1 {
			t.Fatalf("calls = %d", route.Count())
		}
	}
}

func TestDuplicateAfterLostResponseExplainsWhatHappened(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("POST", sendRoute, ts.Reply{Disconnect: true}, ts.JSON(200, ts.Envelope(nil, "A301", "")))
	p := sms(phone)
	p.IdempotencyKey = "k-1"
	_, err := c.Send.SMS(context.Background(), p)
	if !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err.Error(), "이미 접수")

	// without a preceding connection error the server's message is kept
	c2, srv2, _ := newTestClient(t)
	srv2.On("POST", sendRoute, ts.JSON(200, ts.Envelope(nil, "A301", "")))
	_, err = c2.Send.SMS(context.Background(), p)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "Failed" || apiErr.Layer != LayerService {
		t.Fatalf("err = %#v", err)
	}
}

func TestRateLimitIsRetriedWithRetryAfter(t *testing.T) {
	c, srv, d := newTestClient(t)
	limited := ts.JSON(429, map[string]any{"common": map[string]any{"authCode": "A020", "authResult": "Ratelimit"}})
	limited.Header.Set("Retry-After", "2")
	route := srv.On("POST", sendRoute, limited, accepted("A000"))
	if _, err := c.Send.SMS(context.Background(), sms(phone)); err != nil {
		t.Fatal(err)
	}
	if route.Count() != 2 || fmt.Sprint(d.all()) != "[2s]" {
		t.Fatalf("calls = %d, delays = %v", route.Count(), d.all())
	}
}

func TestRateLimitGivesUpAfterMaxRetries(t *testing.T) {
	c, srv, d := newTestClient(t)
	limited := ts.JSON(429, map[string]any{"data": map[string]any{"code": "A020", "result": "Number of 'Ratelimit' exceeded"}})
	limited.Header.Set("Retry-After", "120")
	route := srv.On("POST", sendRoute, limited)
	_, err := c.Send.SMS(context.Background(), sms(phone))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrRateLimit) || apiErr.RetryAfter != time.Minute {
		t.Fatalf("err = %#v", err)
	}
	if route.Count() != 3 { // 1 + max retries (2)
		t.Fatalf("calls = %d", route.Count())
	}
	if fmt.Sprint(d.all()) != "[1m0s 1m0s]" { // Retry-After is capped at 60s
		t.Fatalf("delays = %v", d.all())
	}
}

func TestBackoffWithoutRetryAfter(t *testing.T) {
	for attempt, base := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		for range 20 {
			got := backoff(attempt, 0, false)
			if got < base*3/4 || got > base*5/4 {
				t.Fatalf("attempt %d: %v", attempt, got)
			}
		}
	}
}

func TestClientsKeepTheirOwnKeysConcurrently(t *testing.T) {
	srv := ts.New(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	var wg sync.WaitGroup
	for i := range 8 {
		key := fmt.Sprintf("test-api-key-not-real-%d", i)
		c, err := NewClient(WithAPIKey(key), WithBaseURL(srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p := sms(phone)
				p.Ref = key
				if _, err := c.Send.SMS(context.Background(), p); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	for _, call := range route.Calls() {
		if ref := decodeBody(t, call.Body)["ref"]; call.Header.Get("Authorization") != ref {
			t.Fatalf("key %q sent with ref %v", call.Header.Get("Authorization"), ref)
		}
	}
	if route.Count() != 40 {
		t.Fatalf("calls = %d", route.Count())
	}
}

// The API rejects idempotencyKey without idempotencyTtl (A309, sandbox 2026-09-28), so the SDK fills
// DefaultIdempotencyTTL when only a key is given; an explicit TTL (0 included) is kept, and without a
// key no TTL is added.
func TestIdempotencyTTLDefault(t *testing.T) {
	msg := []ChannelMessage{&SMSMessage{From: phone, Text: "x"}}
	cases := []struct {
		name string
		send func(c *Client) error
		want any // nil: field absent
	}{
		{"omni key only", func(c *Client) error {
			_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: msg, IdempotencyKey: "k"})
			return err
		}, float64(DefaultIdempotencyTTL)},
		{"sms key only", func(c *Client) error {
			_, err := c.Send.SMS(context.Background(), SMSParams{To: To(phone), From: phone, Text: "x", IdempotencyKey: "k"})
			return err
		}, float64(86400)},
		{"explicit ttl", func(c *Client) error {
			_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: msg, IdempotencyKey: "k", IdempotencyTTL: Ptr(600)})
			return err
		}, float64(600)},
		{"explicit zero", func(c *Client) error {
			_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: msg, IdempotencyKey: "k", IdempotencyTTL: Ptr(0)})
			return err
		}, float64(0)},
		{"no key", func(c *Client) error {
			_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: msg})
			return err
		}, nil},
		{"ttl without key", func(c *Client) error {
			_, err := c.Send.Omni(context.Background(), OmniParams{To: To(phone), Messages: msg, IdempotencyTTL: Ptr(60)})
			return err
		}, float64(60)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, srv, _ := newTestClient(t)
			route := srv.On("POST", sendRoute, accepted("A000"))
			if err := tc.send(c); err != nil {
				t.Fatal(err)
			}
			v, ok := decodeBody(t, route.Last(t).Body)["idempotencyTtl"]
			if tc.want == nil && ok || tc.want != nil && v != tc.want {
				t.Fatalf("idempotencyTtl = %v (%v), want %v", v, ok, tc.want)
			}
		})
	}
}

func TestRequestFillsIdempotencyTTLWithoutModifyingTheRequest(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	req := &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{SMS: &SMSMessage{From: phone, Text: "x"}}}, IdempotencyKey: "k"}
	if _, err := c.Send.Request(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if v := decodeBody(t, route.Last(t).Body)["idempotencyTtl"]; v != float64(DefaultIdempotencyTTL) {
		t.Fatalf("idempotencyTtl = %v", v)
	}
	if req.IdempotencyTTL != nil {
		t.Fatal("the caller's request was modified")
	}
}

func TestWithDefaultIdempotencyTTL(t *testing.T) {
	if (*SendOmniRequest)(nil).withDefaultIdempotencyTTL() != nil {
		t.Fatal("nil")
	}
	noKey := &SendOmniRequest{}
	if noKey.withDefaultIdempotencyTTL() != noKey || noKey.IdempotencyTTL != nil {
		t.Fatal("no key: unchanged")
	}
	zero := &SendOmniRequest{IdempotencyKey: "k", IdempotencyTTL: Ptr(0)}
	if got := zero.withDefaultIdempotencyTTL(); got != zero || *got.IdempotencyTTL != 0 {
		t.Fatal("explicit 0 kept")
	}
	keyOnly := &SendOmniRequest{IdempotencyKey: "k", Ref: "r"}
	got := keyOnly.withDefaultIdempotencyTTL()
	if got == keyOnly || keyOnly.IdempotencyTTL != nil || got.GetIdempotencyTTL() != DefaultIdempotencyTTL || got.Ref != "r" || got.IdempotencyKey != "k" {
		t.Fatalf("key only: %+v", got)
	}
}

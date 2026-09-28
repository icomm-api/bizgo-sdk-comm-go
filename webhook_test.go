package bizgo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	webhookSecret = "test-webhook-secret"
	timestampMS   = "1743381600000" // epoch ms, as in the API reference example
)

var (
	webhookNow  = time.UnixMilli(1743381600000)
	webhookBody = []byte(`{"msgKey":"KEY001","serviceType":"SMS","reportTime":"t","reportType":"0","reportCode":"10000"}`)
)

func sign(timestamp, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	return mac.Sum(nil)
}

func TestValidSignatureEncodings(t *testing.T) {
	d := sign(timestampMS, webhookSecret)
	for _, s := range []string{
		hex.EncodeToString(d), strings.ToUpper(hex.EncodeToString(d)), base64.StdEncoding.EncodeToString(d),
		" " + hex.EncodeToString(d) + " ",
	} {
		if err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, DefaultWebhookTolerance, webhookNow); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
}

func TestPrefixedSignatureIsRejected(t *testing.T) {
	d := sign(timestampMS, webhookSecret)
	for _, s := range []string{"sha256=" + hex.EncodeToString(d), "SHA256=" + base64.StdEncoding.EncodeToString(d)} {
		err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, DefaultWebhookTolerance, webhookNow)
		if err == nil {
			t.Fatalf("%q: accepted", s)
		}
		mustContain(t, err.Error(), "일치하지")
	}
}

func TestWrongSecretIsRejected(t *testing.T) {
	err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, hex.EncodeToString(sign(timestampMS, "other")), DefaultWebhookTolerance, webhookNow)
	var we *WebhookVerificationError
	if !errors.As(err, &we) || !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
	mustContain(t, err.Error(), "일치하지")
	mustNotContain(t, err.Error(), webhookSecret)
}

func TestOldTimestampIsRejectedToLimitReplay(t *testing.T) {
	s := hex.EncodeToString(sign(timestampMS, webhookSecret))
	for _, now := range []time.Time{webhookNow.Add(301 * time.Second), webhookNow.Add(-301 * time.Second)} {
		err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, DefaultWebhookTolerance, now)
		mustContain(t, fmt.Sprint(err), "허용 범위")
	}
	if err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, DefaultWebhookTolerance, webhookNow.Add(299*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, NoWebhookTolerance, webhookNow.Add(time.Hour)); err != nil {
		t.Fatal("NoWebhookTolerance disables the age check", err)
	}
	// §12.7: 0 and other non-positive values are mistakes, not "off"
	for _, bad := range []time.Duration{0, -time.Second} {
		if err := VerifyWebhookSignature([]byte(webhookSecret), timestampMS, s, bad, webhookNow); !errors.Is(err, ErrWebhook) {
			t.Fatalf("tolerance %v: %v", bad, err)
		}
		if _, err := NewWebhookReceiver([]byte(webhookSecret), WithWebhookTolerance(bad)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("tolerance %v: %v", bad, err)
		}
	}
}

func TestSecondsTimestampIsAccepted(t *testing.T) {
	if err := VerifyWebhookSignature([]byte(webhookSecret), "1743381600", hex.EncodeToString(sign("1743381600", webhookSecret)), DefaultWebhookTolerance, webhookNow); err != nil {
		t.Fatal(err)
	}
}

func TestMissingOrMalformedHeaders(t *testing.T) {
	good := hex.EncodeToString(sign(timestampMS, webhookSecret))
	for _, tc := range [][2]string{{"", good}, {"abc", good}, {"-1", good}, {"1e3", good}, {timestampMS, ""},
		{timestampMS, "zz"}, {strings.Repeat("9", 30), good}} {
		if err := VerifyWebhookSignature([]byte(webhookSecret), tc[0], tc[1], DefaultWebhookTolerance, webhookNow); !errors.Is(err, ErrWebhook) {
			t.Fatalf("%q: %v", tc, err)
		}
	}
	if err := VerifyWebhookSignature(nil, timestampMS, good, DefaultWebhookTolerance, webhookNow); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
}

func testReceiver(t *testing.T) *WebhookReceiver {
	t.Helper()
	r, err := NewWebhookReceiver([]byte(webhookSecret))
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return webhookNow }
	return r
}

func TestReceiverVerifiesThenParses(t *testing.T) {
	r := testReceiver(t)
	// header names are case-insensitive, also in a hand-built map
	h := http.Header{"x-ib-timestamp": {timestampMS}, "X-IB-SIGNATURE": {hex.EncodeToString(sign(timestampMS, webhookSecret))}}
	payload, err := r.Report(h, webhookBody)
	if err != nil || payload.MsgKey != "KEY001" || payload.ReportCode != "10000" {
		t.Fatalf("%+v %v", payload, err)
	}
	if ack := NewWebhookAck(payload.MsgKey); ack.MsgKey != "KEY001" {
		t.Fatal(ack)
	}
	mustNotContain(t, fmt.Sprintf("%v %+v %#v", r, r, r), webhookSecret)
	if _, err := NewWebhookReceiver([]byte("  ")); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}

func TestReportRequiresSignature(t *testing.T) {
	if _, err := testReceiver(t).Report(http.Header{}, webhookBody); !errors.Is(err, ErrWebhook) {
		t.Fatal(err)
	}
}

func TestParseMOAndInvalidBodies(t *testing.T) {
	mo, err := ParseMOWebhook([]byte(`{"msgKey":"K","serviceType":"MO","msgType":"SM","to":"#000000","from":"01000000000",
		"carrier":"10001","originator":"01000000000","content":"투표 1","occurredTime":"t"}`))
	if err != nil || mo.From != phone || mo.Content != "투표 1" {
		t.Fatalf("%+v %v", mo, err)
	}
	for _, body := range [][]byte{[]byte("not json"), []byte(`[1]`), []byte(`{"msgKey":1}`), make([]byte, MaxWebhookBodyBytes+1)} {
		if _, err := ParseReportWebhook(body); !errors.Is(err, ErrWebhook) {
			t.Fatalf("%.20q: %v", body, err)
		}
	}
}

func TestAckShape(t *testing.T) {
	ack := NewWebhookAck("K")
	if ack.MsgKey != "K" {
		t.Fatal(ack)
	}
}

func TestDefaultToleranceIsFiveMinutes(t *testing.T) {
	if DefaultWebhookTolerance != 5*time.Minute || testReceiver(t).tolerance != DefaultWebhookTolerance {
		t.Fatal(DefaultWebhookTolerance)
	}
}

func TestReportHandler(t *testing.T) {
	r := testReceiver(t)
	var got []string
	h := r.ReportHandler(func(_ context.Context, p *ReportWebhookPayload) error {
		if p.MsgKey == "FAIL" {
			return errors.New("db down")
		}
		got = append(got, p.MsgKey)
		return nil
	})
	send := func(body string, signed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/bizgo/report", strings.NewReader(body))
		if signed {
			req.Header.Set(WebhookTimestampHeader, timestampMS)
			req.Header.Set(WebhookSignatureHeader, hex.EncodeToString(sign(timestampMS, webhookSecret)))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := send(string(webhookBody), true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"msgKey":"KEY001"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := send(string(webhookBody), false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := send("not json", true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := send(`{"msgKey":"FAIL"}`, true); w.Code != 500 || strings.Contains(w.Body.String(), "msgKey") {
		t.Fatal(w.Code) // no ack: Bizgo sends it again
	}
	if w := send(`{"msgKey":"`+strings.Repeat("a", MaxWebhookBodyBytes)+`"}`, true); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if len(got) != 1 || got[0] != "KEY001" {
		t.Fatal(got)
	}
}

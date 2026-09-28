// Command alimtalk-fallback sends a Kakao AlimTalk message; if it fails, Bizgo sends the SMS
// instead (fallback). The idempotency key makes a retry after a timeout safe.
//
//	BIZGO_API_KEY=... BIZGO_FROM=... BIZGO_TO=... BIZGO_KAKAO_SENDER_KEY=... BIZGO_KAKAO_TEMPLATE_CODE=... \
//	  go run ./examples/alimtalk-fallback
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

type config struct {
	from, to, senderKey, templateCode string
}

func run(ctx context.Context, client *bizgo.Client, cfg config, out io.Writer) error {
	orderID := "20260923-0001"
	// The same key makes Bizgo reject a second send, so retries are safe. Derive it from your own
	// data, without putting the phone number in it as is.
	sum := sha256.Sum256([]byte(cfg.to))
	key := "order-" + orderID + "-" + hex.EncodeToString(sum[:8])

	text := "#{name}님, 주문(#{order})이 접수되었습니다." // must match the approved template
	result, err := client.Send.Omni(ctx, bizgo.OmniParams{
		To: []bizgo.Destination{{To: cfg.to, ReplaceWords: map[string]string{"name": "홍길동", "order": orderID}}},
		Messages: []bizgo.ChannelMessage{
			&bizgo.AlimtalkMessage{SenderKey: cfg.senderKey, TemplateCode: cfg.templateCode, MsgType: "AT", Text: text},
			&bizgo.SMSMessage{From: cfg.from, Text: text},
		},
		IdempotencyKey: key,
		Ref:            orderID,
	})
	if errors.Is(err, bizgo.ErrDuplicateRequest) {
		fmt.Fprintln(out, "이미 발송된 주문입니다.")
		return nil
	}
	if err != nil {
		return err
	}
	var failed []string
	for _, d := range result.Failed() {
		failed = append(failed, d.Code)
	}
	fmt.Fprintln(out, "접수:", result.MsgKeys(), "실패:", failed)
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	cfg := config{
		from: env("BIZGO_FROM"), to: env("BIZGO_TO"),
		senderKey: env("BIZGO_KAKAO_SENDER_KEY"), templateCode: env("BIZGO_KAKAO_TEMPLATE_CODE"),
	}
	if err := run(context.Background(), client, cfg, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func env(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("환경변수 %s를 설정하세요 (examples/README.md 참고)", name)
	}
	return v
}

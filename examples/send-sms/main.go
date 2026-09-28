// Command send-sms sends an SMS on the sandbox and prints the per-recipient acceptance result.
//
//	BIZGO_API_KEY=... BIZGO_FROM=... BIZGO_TO=... go run ./examples/send-sms
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func run(ctx context.Context, client *bizgo.Client, from, to string, out io.Writer) error {
	result, err := client.Send.SMS(ctx, bizgo.SMSParams{
		To:   bizgo.To(to),
		From: from,
		Text: "[비즈고] 인증번호는 123456 입니다.",
		Ref:  "signup-otp",
	})
	if err != nil {
		return err
	}
	for _, rejected := range result.Failed() { // accepted != delivered; rejected recipients never get it
		fmt.Fprintln(out, "접수 실패:", rejected.Code, rejected.Result)
	}
	fmt.Fprintln(out, "접수된 메시지 키:", result.MsgKeys()) // the delivery result comes later as a report
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox)) // API key from BIZGO_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, env("BIZGO_FROM"), env("BIZGO_TO"), os.Stdout); err != nil {
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

// Command alimtalk-templates lists the approved AlimTalk templates of a sender profile (all pages)
// and prints their codes and names. It shows the resources generated from the spec:
// client.Alimtalk.Templates, client.Kakao.Senders, client.Insights, client.Reservations, ...
//
//	BIZGO_API_KEY=... BIZGO_KAKAO_SENDER_KEY=... go run ./examples/alimtalk-templates
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func run(ctx context.Context, client *bizgo.Client, senderKey string, out io.Writer) error {
	params := bizgo.ListAlimtalkTemplatesParams{SenderKey: senderKey, InspectionStatus: "APR", Limit: bizgo.Ptr(100)}
	n := 0
	for tpl, err := range client.Alimtalk.Templates.IterList(ctx, params) { // follows offset/limit
		if err != nil {
			return err
		}
		n++
		fmt.Fprintln(out, tpl.TemplateCode, tpl.TemplateName)
	}
	fmt.Fprintln(out, "승인된 템플릿:", n)
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	senderKey := os.Getenv("BIZGO_KAKAO_SENDER_KEY")
	if senderKey == "" {
		log.Fatal("환경변수 BIZGO_KAKAO_SENDER_KEY를 설정하세요 (examples/README.md 참고)")
	}
	if err := run(context.Background(), client, senderKey, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

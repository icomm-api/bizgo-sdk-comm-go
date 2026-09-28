package bizgo_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

// These examples are compiled but not run (they would call the real API). The runnable examples
// in examples/ are executed against a mock server by their tests.

func ExampleNewClient() {
	// The API key is read from BIZGO_API_KEY. Never hard-code it.
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.BaseURL())
}

func ExampleSendService_Omni() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	result, err := client.Send.Omni(context.Background(), bizgo.OmniParams{
		To: []bizgo.Destination{{To: "01000000000", ReplaceWords: map[string]string{"name": "홍길동"}}},
		Messages: []bizgo.ChannelMessage{ // fallback order: AlimTalk first, SMS if it fails
			&bizgo.AlimtalkMessage{SenderKey: "SENDER_KEY_EXAMPLE", TemplateCode: "TEMPLATE_CODE_EXAMPLE", MsgType: "AT",
				Text: "#{name}님, 주문이 접수되었습니다."},
			&bizgo.SMSMessage{From: "01000000000", Text: "#{name}님, 주문이 접수되었습니다."},
		},
		IdempotencyKey: "order-20260923-0001", // retries cannot deliver twice
	})
	var ve *bizgo.ValidationError
	switch {
	case errors.As(err, &ve):
		log.Fatal(ve) // field paths and reasons; nothing was sent
	case errors.Is(err, bizgo.ErrDuplicateRequest):
		log.Print("already accepted")
	case errors.Is(err, bizgo.ErrConnection):
		log.Print("result unknown: check with client.Messages.Status")
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Println(result.MsgKeys(), len(result.Failed()))
	}
}

func ExampleMessagesService_IterHistory() {
	client, err := bizgo.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	since := time.Date(2026, 9, 23, 9, 0, 0, 0, bizgo.KST)
	for m, err := range client.Messages.IterHistory(context.Background(), bizgo.HistoryParams{RequestTime: since}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(m.MsgKey, m.ReportCode)
	}
}

func ExampleWebhookReceiver_ReportHandler() {
	receiver, err := bizgo.NewWebhookReceiver([]byte(os.Getenv("BIZGO_WEBHOOK_SECRET")))
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/bizgo/report", receiver.ReportHandler(func(ctx context.Context, r *bizgo.ReportWebhookPayload) error {
		// store idempotently by r.MsgKey: the same report can arrive again
		return nil
	}))
}

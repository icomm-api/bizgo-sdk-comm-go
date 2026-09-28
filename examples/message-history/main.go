// Command message-history walks the send history of the last hour, then looks up the status of
// one failed message.
//
//	BIZGO_API_KEY=... go run ./examples/message-history
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func run(ctx context.Context, client *bizgo.Client, now time.Time, out io.Writer) error {
	var failed []bizgo.MessageStatus
	params := bizgo.HistoryParams{
		RequestTime:  now.Add(-time.Hour), // converted to KST automatically
		ServiceTypes: []string{bizgo.ServiceTypeSMS, bizgo.ServiceTypeAlimtalk},
	}
	for m, err := range client.Messages.IterHistory(ctx, params) { // follows lastSeq across pages
		if err != nil {
			return err
		}
		if m.ReportCode != "10000" {
			failed = append(failed, m)
		}
	}
	fmt.Fprintf(out, "최근 1시간 실패 %d건\n", len(failed))
	if len(failed) == 0 {
		return nil
	}
	steps, err := client.Messages.Status(ctx, failed[0].MsgKey) // one entry per channel tried (fallback)
	if err != nil {
		return err
	}
	for _, s := range steps {
		fmt.Fprintln(out, s.ServiceType, s.ReportCode, s.ReportText)
	}
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, time.Now(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

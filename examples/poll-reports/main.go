// Command poll-reports processes delivery reports with polling (the API key must be set to
// POLLING in the console). A batch is acknowledged only after it was stored.
//
//	BIZGO_API_KEY=... go run ./examples/poll-reports
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

// store stands in for your database. Upsert by msgKey: a batch is delivered again if saving
// fails before the ack.
type store map[string]string

func (s store) save(_ context.Context, reports []bizgo.Report) error {
	for _, r := range reports {
		if r.ReportCode == "10000" {
			s[r.MsgKey] = "delivered"
		} else {
			s[r.MsgKey] = "failed:" + r.ReportCode
		}
	}
	return nil
}

func run(ctx context.Context, client *bizgo.Client, db store, out io.Writer) error {
	handled, err := client.Reports.Consume(ctx, db.save, nil) // polls, saves, acks only after save succeeded
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "리포트 %d건 처리\n", handled)
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, store{}, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

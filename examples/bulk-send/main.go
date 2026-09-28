// Command bulk-send sends one SMS to a list of recipients of any size on the sandbox, 200 per
// request, with idempotency keys so that running it again does not deliver twice.
//
//	BIZGO_API_KEY=... BIZGO_FROM=... BIZGO_TO=01000000000,01000000001 go run ./examples/bulk-send
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func run(ctx context.Context, client *bizgo.Client, from string, to []string, out io.Writer) error {
	result, err := client.Send.Bulk(ctx, bizgo.BulkParams{
		To:       bizgo.To(to...),
		Messages: []bizgo.ChannelMessage{&bizgo.SMSMessage{From: from, Text: "[비즈고] 9월 이벤트 안내입니다."}},
		// the same list and chunk size produce the same keys: on a re-run, recipients already accepted
		// come back in result.Duplicates() (A301) instead of being sent again
		IdempotencyKeyPrefix: "campaign-sep",
		IdempotencyTTL:       bizgo.Ptr(86400),
	})
	if err != nil {
		return err // invalid request: nothing was sent
	}
	for _, e := range result.Errors { // failed chunks (recipient index ranges, never numbers)
		fmt.Fprintln(out, "청크 실패:", e.Chunk, e.Start, e.End, e.Err)
	}
	fmt.Fprintln(out, "접수:", len(result.Succeeded()), "중복:", len(result.Duplicates()), "거절:", len(result.Failed()))
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox)) // API key from BIZGO_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, env("BIZGO_FROM"), strings.Split(env("BIZGO_TO"), ","), os.Stdout); err != nil {
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

// Command webhook-server receives delivery report webhooks with net/http.
//
//	BIZGO_WEBHOOK_SECRET=... go run ./examples/webhook-server
//
// Production checklist:
//   - serve over HTTPS (put a TLS reverse proxy in front) and allow only the Bizgo webhook source IPs
//   - answer within 5 seconds; do slow work asynchronously
//   - deduplicate by msgKey (Bizgo retries up to 3 times)
//   - confirm important results with the inquiry APIs before acting on them
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

// seen stands in for a persistent deduplication store.
type seen struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (s *seen) handle(_ context.Context, r *bizgo.ReportWebhookPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keys[r.MsgKey] {
		s.keys[r.MsgKey] = true
		// enqueue the report for processing here
	}
	return nil
}

func newMux(receiver *bizgo.WebhookReceiver, s *seen) *http.ServeMux {
	mux := http.NewServeMux()
	// 401 on a bad signature, {"msgKey": ...} once handle returned nil
	mux.Handle("/bizgo/report", receiver.ReportHandler(s.handle))
	return mux
}

func main() {
	receiver, err := bizgo.NewWebhookReceiver([]byte(os.Getenv("BIZGO_WEBHOOK_SECRET")))
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           newMux(receiver, &seen{keys: map[string]bool{}}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	// no access log: request bodies can contain phone numbers
	log.Fatal(server.ListenAndServe())
}

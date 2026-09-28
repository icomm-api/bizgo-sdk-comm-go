package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

func TestRun(t *testing.T) {
	fake := bizgotest.New()
	client, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	to := make([]string, 450)
	for i := range to {
		to[i] = fmt.Sprintf("010%08d", i)
	}
	var out bytes.Buffer
	if err := run(context.Background(), client, "01000000000", to, &out); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if len(reqs) != 3 || !strings.Contains(out.String(), "접수: 450 중복: 0 거절: 0") {
		t.Fatalf("requests=%d out=%s", len(reqs), out.String())
	}
	starts := map[string]bool{}
	for _, r := range reqs { // chunks run concurrently, in any order
		key := r.JSON.(map[string]any)["idempotencyKey"].(string)
		parts := strings.Split(key, "-")
		starts[parts[len(parts)-2]] = strings.HasPrefix(key, "campaign-sep-200-")
	}
	if !starts["0"] || !starts["200"] || !starts["400"] {
		t.Fatal(starts)
	}
}

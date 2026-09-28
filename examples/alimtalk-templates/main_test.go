package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

func TestRun(t *testing.T) {
	fake := bizgotest.New()
	page := func(codes ...string) map[string]any {
		var templates []any
		for _, c := range codes {
			templates = append(templates, map[string]any{"templateCode": c, "templateName": "name-" + c})
		}
		return map[string]any{"alimtalk": map[string]any{"templates": templates}}
	}
	fake.On("listAlimtalkTemplates").Data(page("T1", "T2")).Data(page())
	client, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), client, "SENDER_KEY_EXAMPLE", &out); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || reqs[0].Query.Get("inspectionStatus") != "APR" || !strings.Contains(out.String(), "T2 name-T2") ||
		!strings.Contains(out.String(), "승인된 템플릿: 2") {
		t.Fatalf("requests=%d out=%s", len(reqs), out.String())
	}
}

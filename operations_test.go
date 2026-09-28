package bizgo_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

// These tests go through the public API only, with the bizgotest fake: every operation of the spec
// must be reachable as client.<Resource>.<Method>, send the documented method and path, retry as
// x-sdk-retry says and page as x-sdk-pagination says.

type specFile struct {
	Operations []struct {
		ID, Method, Path, Retry, Rate string
		Pagination                    *struct {
			Style, Request, Size, Items, Response, HasNext, Total string
		}
	} `json:"operations"`
}

func loadOperations(t *testing.T) specFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var s specFile
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// handwrittenCalls call the operations implemented by hand-written convenience methods.
var handwrittenCalls = map[string]func(context.Context, *bizgo.Client) error{
	"sendOmni": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Send.SMS(ctx, bizgo.SMSParams{To: bizgo.To("01000000000"), From: "01000000000", Text: "x"})
		return err
	},
	"uploadMmsFile": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Files.UploadMMS(ctx, bizgo.UploadParams{File: strings.NewReader("jpg"), Filename: "a.jpg"})
		return err
	},
	"uploadRcsFile": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Files.UploadRCS(ctx, bizgo.UploadParams{File: strings.NewReader("jpg"), Filename: "a.jpg"})
		return err
	},
	"getReportPolling": func(ctx context.Context, c *bizgo.Client) error { _, err := c.Reports.Poll(ctx); return err },
	"ackReportPolling": func(ctx context.Context, c *bizgo.Client) error { return c.Reports.Ack(ctx, "PATH-reportId") },
	"getReportInquiry": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Reports.Inquiry(ctx, "PATH-msgKey")
		return err
	},
	"getMessageStatusByMsgKey": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.Status(ctx, "PATH-msgKey")
		return err
	},
	"getMessageStatusByRequestId": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.StatusByRequestID(ctx, "PATH-requestId")
		return err
	},
	"getMessageStatistics": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.Statistics(ctx, bizgo.StatisticsParams{StartDate: bizgo.Date(2026, 1, 2)})
		return err
	},
	"getMessageHistory": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.History(ctx, bizgo.HistoryParams{RequestTime: bizgo.Date(2026, 1, 2)})
		return err
	},
	"getMoByMsgKey": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.MO(ctx, "PATH-msgKey")
		return err
	},
	"getMoHistory": func(ctx context.Context, c *bizgo.Client) error {
		_, err := c.Messages.MOHistory(ctx, bizgo.MOHistoryParams{OccurredTime: bizgo.Date(2026, 1, 2)})
		return err
	},
}

var handwrittenIterators = map[string]func(context.Context, *bizgo.Client) (int, error){
	"getMessageHistory": func(ctx context.Context, c *bizgo.Client) (int, error) {
		n := 0
		for _, err := range c.Messages.IterHistory(ctx, bizgo.HistoryParams{RequestTime: bizgo.Date(2026, 1, 2)}) {
			if err != nil {
				return n, err
			}
			n++
		}
		return n, nil
	},
	"getMoHistory": func(ctx context.Context, c *bizgo.Client) (int, error) {
		n := 0
		for _, err := range c.Messages.IterMOHistory(ctx, bizgo.MOHistoryParams{OccurredTime: bizgo.Date(2026, 1, 2)}) {
			if err != nil {
				return n, err
			}
			n++
		}
		return n, nil
	},
}

// callsFor returns the calls of an operation: the method and, for multipart+JSON operations, the
// WithFiles variant.
func callsFor(id string) map[string]func(context.Context, *bizgo.Client) error {
	out := map[string]func(context.Context, *bizgo.Client) error{}
	if f, ok := handwrittenCalls[id]; ok {
		out[id] = f
	}
	for _, key := range []string{id, id + "+files"} {
		if f, ok := generatedCalls[key]; ok {
			out[key] = func(ctx context.Context, c *bizgo.Client) error {
				_, err := f(ctx, c)
				return err
			}
		}
	}
	return out
}

func expectedPath(template string) string {
	var parts []string
	for _, p := range strings.Split(template, "/") {
		if strings.HasPrefix(p, "{") {
			p = url.PathEscape("PATH-" + strings.Trim(p, "{}"))
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/")
}

func TestEveryOperationIsReachable(t *testing.T) {
	ops := bizgo.Operations()
	if len(ops) != 146 {
		t.Fatalf("%d operations", len(ops))
	}
	total := 0
	for _, op := range ops {
		calls := callsFor(op.ID)
		if len(calls) == 0 {
			t.Errorf("%s (%s) has no method", op.ID, op.Name)
			continue
		}
		for name, call := range calls {
			total++
			t.Run(name, func(t *testing.T) {
				fake := bizgotest.New()
				c, err := fake.Client()
				if err != nil {
					t.Fatal(err)
				}
				if err := call(context.Background(), c); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				reqs := fake.Requests()
				if len(reqs) != 1 {
					t.Fatalf("%d requests", len(reqs))
				}
				r := reqs[0]
				if r.OperationID != op.ID || r.Method != op.Method || r.PathTemplate != op.PathTemplate || r.Path != expectedPath(op.PathTemplate) {
					t.Fatalf("sent %s %s (%s), want %s %s", r.Method, r.Path, r.OperationID, op.Method, expectedPath(op.PathTemplate))
				}
				if strings.HasSuffix(name, "+files") && r.Form == nil {
					t.Fatal("the WithFiles variant did not send multipart/form-data")
				}
			})
		}
	}
	if total != len(generatedCalls)+len(handwrittenCalls) {
		t.Fatalf("%d calls made, %d defined", total, len(generatedCalls)+len(handwrittenCalls))
	}
}

func TestEveryOperationRetriesAsTheSpecSays(t *testing.T) {
	for _, op := range bizgo.Operations() {
		for name, call := range callsFor(op.ID) {
			fake := bizgotest.New()
			fake.On(op.ID).RespondWithHeader(503, "busy", http.Header{"Retry-After": {"0"}})
			c, err := fake.Client(bizgo.WithMaxRetries(2))
			if err != nil {
				t.Fatal(err)
			}
			err = call(context.Background(), c)
			if !errors.Is(err, bizgo.ErrInternalServer) {
				t.Fatalf("%s: %v", name, err)
			}
			want := 1 // rate_limit_only: 503 is not retried (the request may have been processed)
			if op.Retry == "safe" {
				want = 3
			}
			if n := len(fake.Requests()); n != want {
				t.Errorf("%s (%s): %d attempts, want %d", name, op.Retry, n, want)
			}
			// 429 is retried by every operation
			fake = bizgotest.New()
			fake.On(op.ID).Fail(bizgo.LayerGateway, 429, "A020").Default()
			c, _ = fake.Client()
			if err := call(context.Background(), c); err != nil || len(fake.Requests()) != 2 {
				t.Errorf("%s: 429 not retried: %v, %d attempts", name, err, len(fake.Requests()))
			}
		}
	}
}

// setPath puts value at a dotted response path ("data.data.x[-1].y" puts it in the last item).
func setPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := root
	for i, part := range parts {
		last := i == len(parts)-1
		if name, ok := strings.CutSuffix(part, "[-1]"); ok {
			items, _ := cur[name].([]any)
			if len(items) == 0 {
				items = []any{map[string]any{}}
				cur[name] = items
			}
			cur = items[len(items)-1].(map[string]any)
			continue
		}
		if last {
			cur[part] = value
			return
		}
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
}

func page(items []any, set func(map[string]any)) map[string]any {
	env := bizgotest.SuccessEnvelope(nil, nil)
	if set != nil {
		set(env)
	}
	return env
}

func iteratorFor(id string) func(context.Context, *bizgo.Client) (int, error) {
	if f, ok := generatedIterators[id]; ok {
		return f
	}
	return handwrittenIterators[id]
}

func TestEveryPaginatedOperationIterates(t *testing.T) {
	n := 0
	for _, op := range loadOperations(t).Operations {
		pg := op.Pagination
		if pg == nil {
			continue
		}
		n++
		iterate := iteratorFor(op.ID)
		if iterate == nil {
			t.Fatalf("%s has no Iter method", op.ID)
		}
		var item any
		if err := json.Unmarshal([]byte(pageItemSamples[op.ID]), &item); err != nil {
			t.Fatalf("%s: no item sample", op.ID)
		}
		t.Run(op.ID, func(t *testing.T) {
			fake := bizgotest.New()
			switch pg.Style {
			case "cursor":
				cursor := any(7)
				if strings.Contains(pg.Response, "[-1]") {
					cursor = "CURSOR-7"
				}
				first := page(nil, func(env map[string]any) {
					setPath(env, pg.Items, []any{item, clone(item)})
					setPath(env, pg.Response, cursor)
					if pg.HasNext != "" {
						setPath(env, pg.HasNext, true)
					}
				})
				second := page(nil, func(env map[string]any) {
					setPath(env, pg.Items, []any{clone(item)})
					if pg.HasNext != "" {
						setPath(env, pg.HasNext, false)
					}
				})
				fake.On(op.ID).Respond(200, first).Respond(200, second)
				got, err := iterate(context.Background(), mustClient(t, fake))
				reqs := fake.Requests()
				if err != nil || got != 3 || len(reqs) != 2 {
					t.Fatalf("items %d, requests %d, err %v", got, len(reqs), err)
				}
				if reqs[0].Query.Has(pg.Request) || reqs[1].Query.Get(pg.Request) != strings.TrimPrefix(toString(cursor), "") {
					t.Fatalf("cursor %q, want %v", reqs[1].Query.Get(pg.Request), cursor)
				}
			default: // page, offset: a full page, then an empty one
				fake.On(op.ID).
					Respond(200, page(nil, func(env map[string]any) { setPath(env, pg.Items, []any{item, clone(item)}) })).
					Respond(200, page(nil, func(env map[string]any) { setPath(env, pg.Items, []any{}) }))
				got, err := iterate(context.Background(), mustClient(t, fake))
				reqs := fake.Requests()
				if err != nil || got != 2 || len(reqs) != 2 {
					t.Fatalf("items %d, requests %d, err %v", got, len(reqs), err)
				}
				first, second := "1", "2"
				if pg.Style == "offset" {
					first, second = "0", "2"
				}
				if reqs[0].Query.Get(pg.Request) != first || reqs[1].Query.Get(pg.Request) != second {
					t.Fatalf("%s: %q then %q", pg.Request, reqs[0].Query.Get(pg.Request), reqs[1].Query.Get(pg.Request))
				}
				if pg.Total != "" { // stops when the total is reached, without an extra request
					fake := bizgotest.New()
					fake.On(op.ID).Respond(200, page(nil, func(env map[string]any) {
						setPath(env, pg.Items, []any{item, clone(item)})
						setPath(env, pg.Total, 2)
						if strings.HasSuffix(pg.Total, "pagination.total") {
							setPath(env, pg.Total, "2") // documented as a string
						}
					}))
					got, err := iterate(context.Background(), mustClient(t, fake))
					if err != nil || got != 2 || len(fake.Requests()) != 1 {
						t.Fatalf("total: items %d, requests %d, err %v", got, len(fake.Requests()), err)
					}
				}
				if pg.HasNext != "" { // stops on hasNext false
					fake := bizgotest.New()
					fake.On(op.ID).Respond(200, page(nil, func(env map[string]any) {
						setPath(env, pg.Items, []any{item})
						setPath(env, pg.HasNext, false)
					}))
					got, err := iterate(context.Background(), mustClient(t, fake))
					if err != nil || got != 1 || len(fake.Requests()) != 1 {
						t.Fatalf("hasNext: items %d, requests %d, err %v", got, len(fake.Requests()), err)
					}
				}
			}
			// an error is yielded once, at the end
			fake = bizgotest.New()
			fake.On(op.ID).Fail(bizgo.LayerGateway, 401, "A401")
			if _, err := iterate(context.Background(), mustClient(t, fake)); !errors.Is(err, bizgo.ErrAuthentication) {
				t.Fatal(err)
			}
		})
	}
	if n != len(pageItemSamples) || n != 14 {
		t.Fatalf("%d paginated operations, %d samples", n, len(pageItemSamples))
	}
}

func clone(v any) any {
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	}
	return ""
}

func mustClient(t *testing.T, fake *bizgotest.Fake, opts ...bizgo.Option) *bizgo.Client {
	t.Helper()
	c, err := fake.Client(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The iterator never changes the caller's page value.
func TestIteratorDoesNotModifyTheCallersParams(t *testing.T) {
	fake := bizgotest.New()
	fake.On("listAlimtalkTemplates").
		Data(map[string]any{"alimtalk": map[string]any{"templates": []any{map[string]any{}}}}).
		Data(map[string]any{"alimtalk": map[string]any{"templates": []any{}}})
	offset := 5
	params := bizgo.ListAlimtalkTemplatesParams{SenderKey: "SENDER_KEY_EXAMPLE", Offset: &offset}
	for _, err := range mustClient(t, fake).Alimtalk.Templates.IterList(context.Background(), params) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if offset != 5 || fake.Requests()[1].Query.Get("offset") != "6" {
		t.Fatalf("offset %d, second request %q", offset, fake.Requests()[1].Query.Get("offset"))
	}
}

func TestGeneratedMethodsValidateBeforeSending(t *testing.T) {
	fake := bizgotest.New()
	c := mustClient(t, fake)
	ctx := context.Background()
	// required query parameter
	_, err := c.Alimtalk.Templates.List(ctx, bizgo.ListAlimtalkTemplatesParams{})
	var ve *bizgo.ValidationError
	if !errors.As(err, &ve) || ve.Problems[0].Path != "senderKey" {
		t.Fatal(err)
	}
	// enum and maximum
	_, err = c.Alimtalk.Templates.List(ctx, bizgo.ListAlimtalkTemplatesParams{SenderKey: "SENDER_KEY_EXAMPLE", SenderKeyType: "X", Limit: bizgo.Ptr(0)})
	if !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatal(err)
	}
	// nil and invalid bodies
	if _, err := c.Reservations.Create(ctx, nil); !errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := c.Reservations.Create(ctx, &bizgo.ReservationCreateRequest{}); !errors.As(err, &ve) || len(ve.Problems) < 3 {
		t.Fatal(err)
	}
	// path values: empty, "." and ".." are rejected (§12.5)
	for _, bad := range []string{"", ".", ".."} {
		if err := c.Alimtalk.Templates.Delete(ctx, bad, "TEMPLATE"); !errors.Is(err, bizgo.ErrValidation) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	// a header parameter cannot carry a new line
	_, err = c.Kakao.Senders.Create(ctx, &bizgo.KakaoSenderCreateRequest{}, bizgo.CreateKakaoSenderParams{Token: "a\r\nX-Injected: 1", PhoneNumber: "01000000000"})
	if !errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
	if len(fake.Requests()) != 0 {
		t.Fatalf("%d requests were sent", len(fake.Requests()))
	}
	// other values are escaped into one segment
	if err := c.Alimtalk.Templates.Delete(ctx, "a/b?c#d%e", "T"); err != nil {
		t.Fatal(err)
	}
	if p := fake.LastRequest().Path; !strings.Contains(p, "/senderKey/a%2Fb%3Fc%23d%25e/templateCode/T") {
		t.Fatal(p)
	}
}

func TestGeneratedQueryParameters(t *testing.T) {
	fake := bizgotest.New()
	c := mustClient(t, fake)
	ctx := context.Background()
	start := time.Date(2026, 4, 1, 0, 30, 0, 0, time.UTC) // 09:30 KST, same day
	_, err := c.Insights.Alimtalk.Get(ctx, bizgo.GetAlimtalkInsightParams{
		StartDate: start, EndDate: start.Add(24 * time.Hour), SenderKey: []string{"KEY_A", "KEY_B"},
	})
	if err != nil {
		t.Fatal(err)
	}
	q := fake.LastRequest().Query
	if q.Get("startDate") != "20260401" || q.Get("endDate") != "20260402" || q.Get("senderKey") != "KEY_A,KEY_B" {
		t.Fatal(q)
	}
	// header parameters are sent as headers (values are never recorded, only names)
	if _, err := c.Kakao.Senders.Create(ctx, &bizgo.KakaoSenderCreateRequest{}, bizgo.CreateKakaoSenderParams{Token: "123456", PhoneNumber: "01000000000"}); err != nil &&
		!errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
}

func TestMultipartOperation(t *testing.T) {
	fake := bizgotest.New()
	c := mustClient(t, fake)
	_, err := c.RCS.Brands.Update(context.Background(), &bizgo.RCSBrandUpdateRequest{
		BrandID:      "BRAND_ID_EXAMPLE",
		RegBrand:     &bizgo.RCSBrandUpdateRequestRegBrand{BrandID: "BRAND_ID_EXAMPLE", Name: "brand"},
		BrandProfile: &bizgo.UploadFile{Reader: strings.NewReader("png-bytes"), Filename: "logo.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	form := fake.LastRequest().Form
	if form["brandId"][0].Text != "BRAND_ID_EXAMPLE" || form["brandProfile"][0].Filename != "logo.png" ||
		form["brandProfile"][0].ContentType != "image/png" || form["brandProfile"][0].Size != 9 {
		t.Fatalf("%+v", form)
	}
	if reg := form["regBrand"][0]; reg.ContentType != "application/json" || !strings.Contains(reg.Text, `"name":"brand"`) {
		t.Fatalf("%+v", reg)
	}
	// a required file is checked before sending; an unreadable file names the file only (§12.18)
	_, err = c.RCS.Chatbots.Update(context.Background(), &bizgo.RCSChatbotUpdateRequest{BrandID: "B", Chatbot: &bizgo.RCSChatbot{}})
	if !errors.Is(err, bizgo.ErrValidation) {
		t.Fatal(err)
	}
	missing := t.TempDir() + "/secret-dir/certificate.pdf"
	_, err = c.RCS.Chatbots.Update(context.Background(), &bizgo.RCSChatbotUpdateRequest{
		BrandID: "B", Chatbot: &bizgo.RCSChatbot{}, SubNumCertificate: &bizgo.UploadFile{Path: missing},
	})
	if !errors.Is(err, bizgo.ErrValidation) || !strings.Contains(err.Error(), "certificate.pdf") || strings.Contains(err.Error(), "secret-dir") {
		t.Fatal(err)
	}
}

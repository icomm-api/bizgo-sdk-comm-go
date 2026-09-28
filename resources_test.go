package bizgo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ts "github.com/icomm-api/bizgo-sdk-comm-go/internal/testserver"
)

const (
	pollRoute    = "/api/comm/v1/report/polling"
	historyRoute = "/api/comm/v1/message/history"
)

var report = map[string]any{"msgKey": "KEY001", "serviceType": "SMS", "msgType": "SM", "reportCode": "10000", "reportType": "0"}

func TestConsumeAcksEachBatchAfterTheHandler(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", pollRoute,
		ts.OK(map[string]any{"reportId": "R1", "report": []any{report, report}}),
		ts.OK(map[string]any{"reportId": "", "report": nil}))
	ack := srv.On("DELETE", pollRoute+"/R1", ts.OK(nil))
	var seen []string
	n, err := c.Reports.Consume(context.Background(), func(_ context.Context, reports []Report) error {
		for _, r := range reports {
			seen = append(seen, r.MsgKey)
		}
		return nil
	}, nil)
	if err != nil || n != 2 || strings.Join(seen, ",") != "KEY001,KEY001" || ack.Count() != 1 {
		t.Fatalf("n=%d err=%v seen=%v acks=%d", n, err, seen, ack.Count())
	}
}

func TestConsumeDoesNotAckWhenTheHandlerFails(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", pollRoute, ts.OK(map[string]any{"reportId": "R1", "report": []any{report}}))
	ack := srv.On("DELETE", pollRoute+"/R1", ts.OK(nil))
	failure := errors.New("db down")
	_, err := c.Reports.Consume(context.Background(), func(context.Context, []Report) error { return failure }, nil)
	if !errors.Is(err, failure) || ack.Count() != 0 {
		t.Fatalf("err=%v acks=%d", err, ack.Count())
	}
}

func TestConsumeStopsAfterMaxBatches(t *testing.T) {
	c, srv, _ := newTestClient(t)
	poll := srv.On("GET", pollRoute, ts.OK(map[string]any{"reportId": "R1", "report": []any{report}}))
	srv.On("DELETE", pollRoute+"/R1", ts.OK(nil))
	n, err := c.Reports.Consume(context.Background(), func(context.Context, []Report) error { return nil }, &ConsumeOptions{MaxBatches: 2})
	if err != nil || n != 2 || poll.Count() != 2 {
		t.Fatalf("n=%d err=%v polls=%d", n, err, poll.Count())
	}
}

func TestPollReturnsEmptyBatch(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", pollRoute, ts.OK(map[string]any{"reportId": "", "report": nil}))
	batch, err := c.Reports.Poll(context.Background())
	if err != nil || !batch.Empty() || batch.ReportID != "" {
		t.Fatalf("%+v %v", batch, err)
	}
}

func TestPathParametersAreEscaped(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", "/api/comm/v1/report/inquiry/a%2Fb%3Fc%23d", ts.OK(map[string]any{"report": []any{report}}))
	reports, err := c.Reports.Inquiry(context.Background(), "a/b?c#d")
	if err != nil || len(reports) != 1 || route.Count() != 1 || route.Last(t).RawQuery != "" {
		t.Fatalf("%v %v unrouted=%v", reports, err, srv.Unrouted())
	}
}

func TestDotAndEmptySegmentsAreRejected(t *testing.T) {
	c, srv, _ := newTestClient(t)
	ctx := context.Background()
	for _, v := range []string{"", ".", ".."} {
		if _, err := c.Reports.Inquiry(ctx, v); !errors.Is(err, ErrValidation) {
			t.Fatalf("%q: %v", v, err)
		}
		if err := c.Reports.Ack(ctx, v); !errors.Is(err, ErrValidation) {
			t.Fatalf("%q: %v", v, err)
		}
		if _, err := c.Messages.Status(ctx, v); !errors.Is(err, ErrValidation) {
			t.Fatalf("%q: %v", v, err)
		}
	}
	if len(srv.Unrouted()) != 0 {
		t.Fatal("a request was sent")
	}
}

func TestStatusQueriesUseTheirPaths(t *testing.T) {
	c, srv, _ := newTestClient(t)
	ctx := context.Background()
	msg := map[string]any{"messages": []any{map[string]any{"msgKey": "K", "serviceType": "SMS"}}}
	byKey := srv.On("GET", "/api/comm/v1/message/inquiry/msgKey/K", ts.OK(msg))
	byReq := srv.On("GET", "/api/comm/v1/message/inquiry/requestId/R", ts.OK(msg))
	mo := srv.On("GET", "/api/comm/v1/message/inquiry/mo/msgKey/M", ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "M", "from": phone}}}))
	s1, err1 := c.Messages.Status(ctx, "K")
	s2, err2 := c.Messages.StatusByRequestID(ctx, "R")
	m, err3 := c.Messages.MO(ctx, "M")
	if err := errors.Join(err1, err2, err3); err != nil {
		t.Fatal(err)
	}
	if len(s1) != 1 || len(s2) != 1 || len(m) != 1 || m[0].From != phone || byKey.Count()+byReq.Count()+mo.Count() != 3 {
		t.Fatal("unexpected result")
	}
}

func TestIterHistoryFollowsLastSeq(t *testing.T) {
	c, srv, _ := newTestClient(t)
	msg := map[string]any{"msgKey": "K", "serviceType": "SMS"}
	route := srv.On("GET", historyRoute,
		ts.OK(map[string]any{"messages": []any{msg, msg}, "lastSeq": 10, "hasNext": true}),
		ts.OK(map[string]any{"messages": []any{msg}, "lastSeq": 11, "hasNext": false}))
	n := 0
	for m, err := range c.Messages.IterHistory(context.Background(), HistoryParams{
		RequestTime: time.Date(2026, 9, 23, 9, 0, 0, 0, KST), ServiceTypes: []string{"SMS", "RCS"}, Limit: 2,
	}) {
		if err != nil || m.MsgKey != "K" {
			t.Fatal(err)
		}
		n++
	}
	calls := route.Calls()
	if n != 3 || len(calls) != 2 {
		t.Fatalf("items=%d calls=%d", n, len(calls))
	}
	first, second := calls[0].Query, calls[1].Query
	if first.Get("requestTime") != "2026-09-23T09:00:00" || first.Get("serviceType") != "SMS,RCS" || first.Get("limit") != "2" ||
		first.Has("lastSeq") || second.Get("lastSeq") != "10" {
		t.Fatalf("queries: %v / %v", first, second)
	}
}

func TestIterHistoryStopsIfTheCursorDoesNotMoveOrIsMissing(t *testing.T) {
	for _, page := range []map[string]any{
		{"messages": []any{map[string]any{"msgKey": "K"}}, "lastSeq": 5, "hasNext": true},
		{"messages": []any{map[string]any{"msgKey": "K"}}, "hasNext": true},
	} {
		c, srv, _ := newTestClient(t)
		route := srv.On("GET", historyRoute, ts.OK(page))
		n := 0
		for _, err := range c.Messages.IterHistory(context.Background(), HistoryParams{RequestTime: time.Now()}) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		want := 2
		if _, ok := page["lastSeq"]; !ok {
			want = 1
		}
		if n != want || route.Count() != want {
			t.Fatalf("items=%d calls=%d", n, route.Count())
		}
	}
}

func TestIterationCanStopEarlyAndReportsErrors(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", moRoute,
		ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "M1"}, map[string]any{"msgKey": "M2"}}, "lastSeq": 1, "hasNext": true}),
		ts.JSON(400, ts.Envelope(nil, "A306", "")))
	for m := range c.Messages.IterMOHistory(context.Background(), MOHistoryParams{OccurredTime: time.Now()}) {
		if m.MsgKey != "M1" {
			t.Fatal(m)
		}
		break
	}
	if route.Count() != 1 {
		t.Fatalf("calls = %d", route.Count())
	}
	srv.On("GET", moRoute,
		ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "M1"}, map[string]any{"msgKey": "M2"}}, "lastSeq": 1, "hasNext": true}),
		ts.JSON(400, ts.Envelope(nil, "A306", "")))
	var keys []string
	var last error
	for m, err := range c.Messages.IterMOHistory(context.Background(), MOHistoryParams{OccurredTime: time.Now()}) {
		if err != nil {
			last = err
			continue
		}
		keys = append(keys, m.MsgKey)
	}
	if !errors.Is(last, ErrBadRequest) || strings.Join(keys, ",") != "M1,M2" {
		t.Fatalf("keys=%v err=%v", keys, last)
	}
}

func TestIterMOHistoryWalksPages(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", moRoute,
		ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "M1"}}, "lastSeq": 1, "hasNext": true}),
		ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "M2"}}, "lastSeq": 2, "hasNext": false}))
	var keys []string
	for m, err := range c.Messages.IterMOHistory(context.Background(), MOHistoryParams{OccurredTime: time.Now()}) {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, m.MsgKey)
	}
	if strings.Join(keys, ",") != "M1,M2" {
		t.Fatal(keys)
	}
}

func TestMOHistoryTimeIsSentWithKSTOffset(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", moRoute, ts.OK(map[string]any{"messages": []any{}, "hasNext": false}))
	ctx := context.Background()
	for _, tm := range []time.Time{time.Date(2026, 4, 23, 14, 11, 1, 0, KST), time.Date(2026, 4, 23, 5, 11, 1, 0, time.UTC)} {
		if _, err := c.Messages.MOHistory(ctx, MOHistoryParams{OccurredTime: tm, From: otherPhone, To: phone}); err != nil {
			t.Fatal(err)
		}
		q := route.Last(t).Query
		if q.Get("occurredTime") != "2026-04-23T14:11:01+09:00" || q.Get("from") != otherPhone || q.Get("to") != phone {
			t.Fatal(q)
		}
	}
}

func TestAwareTimesAreConvertedToKST(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", historyRoute, ts.OK(map[string]any{"messages": []any{}}))
	if _, err := c.Messages.History(context.Background(), HistoryParams{RequestTime: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if got := route.Last(t).Query.Get("requestTime"); got != "2026-09-23T09:00:00" {
		t.Fatal(got)
	}
}

func TestLimitRangeIsCheckedBeforeSending(t *testing.T) {
	c, srv, _ := newTestClient(t)
	ctx := context.Background()
	for _, limit := range []int{-1, 1001} {
		_, err := c.Messages.History(ctx, HistoryParams{RequestTime: time.Now(), Limit: limit})
		mustContain(t, fmt.Sprint(err), "1~1000")
		_, err = c.Messages.MOHistory(ctx, MOHistoryParams{OccurredTime: time.Now(), Limit: limit})
		mustContain(t, fmt.Sprint(err), "1~1000")
	}
	if _, err := c.Messages.History(ctx, HistoryParams{}); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	if len(srv.Unrouted()) != 0 {
		t.Fatal("a request was sent")
	}
}

func TestStatisticsParameters(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("GET", statsRoute, ts.OK(map[string]any{"statistics": []any{map[string]any{"statDate": "20260923", "recvTotalCnt": 3}}}))
	got, err := c.Messages.Statistics(context.Background(), StatisticsParams{
		StartDate: Date(2026, 9, 1), EndDate: time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC), ServiceType: ServiceTypeSMS,
	})
	if err != nil || len(got) != 1 || got[0].GetRecvTotalCnt() != 3 || got[0].StatDate != "20260923" {
		t.Fatalf("%+v %v", got, err)
	}
	if q := route.Last(t).RawQuery; q != "endDate=20260923&serviceType=SMS&startDate=20260901" {
		t.Fatal(q)
	}
	if _, err := c.Messages.Statistics(context.Background(), StatisticsParams{}); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
}

func TestResponseKeepsUnknownFields(t *testing.T) {
	c, srv, _ := newTestClient(t)
	srv.On("GET", "/api/comm/v1/message/inquiry/msgKey/K",
		ts.OK(map[string]any{"messages": []any{map[string]any{"msgKey": "K", "newServerField": "v"}}, "other": 1}))
	got, err := c.Messages.Status(context.Background(), "K")
	if err != nil || len(got) != 1 || got[0].MsgKey != "K" || string(got[0].Extra["newServerField"]) != `"v"` || len(got[0].Extra) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func readPart(t *testing.T, call ts.Call) (*multipart.Part, []byte, map[string]string) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(call.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type = %q", call.Header.Get("Content-Type"))
	}
	r := multipart.NewReader(bytes.NewReader(call.Body), params["boundary"])
	var file *multipart.Part
	var content []byte
	fields := map[string]string{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			file, content = p, data
		} else {
			fields[p.FormName()] = string(data)
		}
	}
	if file == nil {
		t.Fatal("no file part")
	}
	return file, content, fields
}

func TestUploadMMSSendsMultipart(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/mms", ts.OK(map[string]any{"fileKey": "FILE_KEY_001", "expired": "2027-04-24T10:45:24+09:00"}))
	result, err := c.Files.UploadMMS(context.Background(), UploadParams{File: strings.NewReader("\xff\xd8jpeg-bytes"), Filename: "a.jpg", ImageName: "banner"})
	if err != nil || result.FileKey != "FILE_KEY_001" || result.Expired == "" {
		t.Fatalf("%+v %v", result, err)
	}
	part, content, fields := readPart(t, route.Last(t))
	if part.FileName() != "a.jpg" || part.Header.Get("Content-Type") != "image/jpeg" || string(content) != "\xff\xd8jpeg-bytes" {
		t.Fatalf("part = %v", part.Header)
	}
	if fields["imageName"] != "banner" || len(fields) != 1 {
		t.Fatal(fields)
	}
	if route.Last(t).Header.Get("Authorization") != apiKey {
		t.Fatal("no key")
	}
}

func TestUploadMMSSizeLimitIsCheckedBeforeSending(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/mms", ts.OK(nil))
	_, err := c.Files.UploadMMS(context.Background(), UploadParams{File: bytes.NewReader(make([]byte, MMSMaxBytes+1)), Filename: "a.jpg"})
	mustContain(t, fmt.Sprint(err), "300KB")
	if _, err := c.Files.UploadMMS(context.Background(), UploadParams{File: bytes.NewReader(make([]byte, MMSMaxBytes)), Filename: "a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Files.UploadMMS(context.Background(), UploadParams{}); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	if route.Count() != 1 {
		t.Fatalf("calls = %d", route.Count())
	}
}

func TestUploadFromFileUsesTheFileName(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/rcs", ts.OK(map[string]any{"media": "maapfile://MEDIA_KEY_EXAMPLE"}))
	path := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nrest"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	result, err := c.Files.UploadRCS(context.Background(), UploadParams{File: f})
	if err != nil || result.Media != "maapfile://MEDIA_KEY_EXAMPLE" {
		t.Fatalf("%+v %v", result, err)
	}
	part, _, _ := readPart(t, route.Last(t))
	if part.FileName() != "card.png" || part.Header.Get("Content-Type") != "image/png" {
		t.Fatal(part.Header)
	}
}

func TestFileNamesCannotInjectHeaders(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/mms", ts.OK(nil))
	_, err := c.Files.UploadMMS(context.Background(), UploadParams{File: strings.NewReader("img"), Filename: "a\"\r\nX-Injected: 1\r\n.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	part, _, _ := readPart(t, route.Last(t))
	if part.Header.Get("X-Injected") != "" || strings.ContainsAny(part.FileName(), "\r\n") {
		t.Fatalf("header injection: %v", part.Header)
	}
}

func TestUploadBrandMessagePathAndKindWhitelist(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/brandmessage/wideItemList/first", ts.OK(map[string]any{"imgUrl": "https://example.com/img.jpg"}))
	result, err := c.Files.UploadBrandMessage(context.Background(), BrandImageWideItemListFirst, UploadParams{File: strings.NewReader("img"), Filename: "a.png"})
	if err != nil || result.ImgURL != "https://example.com/img.jpg" || route.Count() != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, kind := range []BrandImageKind{"../etc", "", "wide/../default", "WIDE"} {
		if _, err := c.Files.UploadBrandMessage(context.Background(), kind, UploadParams{File: strings.NewReader("img")}); !errors.Is(err, ErrValidation) {
			t.Fatalf("%q: %v", kind, err)
		}
	}
	if len(srv.Unrouted()) != 0 {
		t.Fatal("a request was sent")
	}
}

func TestUploadsAreNotRetriedOnServerErrorsButFileIsResentOn429(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", "/api/comm/v1/file/mms", ts.Text(503, "busy"))
	if _, err := c.Files.UploadMMS(context.Background(), UploadParams{File: strings.NewReader("img")}); !errors.Is(err, ErrInternalServer) {
		t.Fatal(err)
	}
	if route.Count() != 1 {
		t.Fatalf("calls = %d", route.Count())
	}
	route = srv.On("POST", "/api/comm/v1/file/mms", ts.JSON(429, ts.Envelope(nil, "A020", "")), ts.OK(map[string]any{"fileKey": "F"}))
	if _, err := c.Files.UploadMMS(context.Background(), UploadParams{File: strings.NewReader("img-content")}); err != nil {
		t.Fatal(err)
	}
	calls := route.Calls()
	if len(calls) != 2 || !bytes.Equal(calls[0].Body, calls[1].Body) || !bytes.Contains(calls[1].Body, []byte("img-content")) {
		t.Fatal("the file was not resent as is")
	}
}

func TestAckIsRetriedOnServerErrors(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("DELETE", pollRoute+"/R1", ts.Text(503, "busy"), ts.OK(nil))
	if err := c.Reports.Ack(context.Background(), "R1"); err != nil || route.Count() != 2 {
		t.Fatalf("%v calls=%d", err, route.Count())
	}
}

package bizgo

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// testdata/spec.json is generated from spec/openapi.yaml by tools/gen (checked in CI).
type specOperation struct {
	ID, Method, Path, Resource, SDKMethod, Retry, Rate, Result string
	Pagination                                                 *struct {
		Style, Request, Size, Items, Response, HasNext, Total string
	}
}

type specData struct {
	SpecVersion string          `json:"specVersion"`
	Operations  []specOperation `json:"operations"`
	Webhooks    []struct {
		Name, ID, Payload string
		Signed            bool
		Example           json.RawMessage
	} `json:"webhooks"`
	Schemas          []string                   `json:"schemas"`
	RequestExamples  map[string]json.RawMessage `json:"requestExamples"`
	ResponseExamples []struct {
		Operation, Status, Schema string
		Value                     json.RawMessage
	} `json:"responseExamples"`
}

func loadSpec(t *testing.T) specData {
	t.Helper()
	raw, err := os.ReadFile("testdata/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var s specData
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSpecRequestExamplesValidateAndRoundTrip(t *testing.T) {
	s := loadSpec(t)
	if len(s.RequestExamples) == 0 {
		t.Fatal("no examples")
	}
	for name, value := range s.RequestExamples {
		req, err := ParseSendOmniRequest(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		jsonEqual(t, out, string(value))
	}
}

func TestSpecResponseExamplesParse(t *testing.T) {
	s := loadSpec(t)
	if len(s.ResponseExamples) < 50 {
		t.Fatalf("only %d examples", len(s.ResponseExamples))
	}
	for _, ex := range s.ResponseExamples {
		model, ok := specSchemas[ex.Schema]
		if !ok {
			t.Fatalf("%s: no model for %s", ex.Operation, ex.Schema)
		}
		v := reflect.New(reflect.TypeOf(model).Elem()).Interface()
		if err := json.Unmarshal(ex.Value, v); err != nil {
			t.Fatalf("%s: %v", ex.Operation, err)
		}
		if _, err := checkResponse(200, nil, ex.Value); err != nil {
			t.Fatalf("%s: %v", ex.Operation, err)
		}
		// nothing of the documented example ends up in Extra
		if extras := collectExtra(reflect.ValueOf(v)); len(extras) > 0 {
			t.Errorf("%s (%s): fields not in the spec: %v", ex.Operation, ex.Schema, extras)
		}
	}
}

// collectExtra returns the names of the Extra fields that are set anywhere in a decoded model.
func collectExtra(v reflect.Value) []string {
	var out []string
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			out = append(out, collectExtra(v.Elem())...)
		}
	case reflect.Slice:
		for i := range v.Len() {
			out = append(out, collectExtra(v.Index(i))...)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Name == "Extra" && v.Field(i).Len() > 0 {
				for _, k := range v.Field(i).MapKeys() {
					out = append(out, v.Type().Name()+"."+k.String())
				}
				continue
			}
			out = append(out, collectExtra(v.Field(i))...)
		}
	}
	return out
}

// Every operation of the spec is in the generated operation table with the metadata of the spec.
// (operations_test.go calls every one of them through the public API.)
func TestOperationTableMatchesTheSpec(t *testing.T) {
	spec := loadSpec(t).Operations
	if len(spec) != len(operationTable) || len(spec) != 146 {
		t.Fatalf("%d operations in the spec, %d in the table", len(spec), len(operationTable))
	}
	byID := map[string]*operation{}
	for _, op := range operationTable {
		byID[op.id] = op
	}
	sendBucket := 0
	for _, s := range spec {
		op := byID[s.ID]
		if op == nil {
			t.Fatalf("%s is missing", s.ID)
		}
		info := op.info()
		style := ""
		if s.Pagination != nil {
			style = s.Pagination.Style
		}
		want := OperationInfo{ID: s.ID, Name: s.Resource + "." + s.SDKMethod, Method: s.Method, PathTemplate: s.Path,
			Retry: s.Retry, RateBucket: s.Rate, Pagination: style}
		if info != want {
			t.Errorf("%s:\n got %+v\nwant %+v", s.ID, info, want)
		}
		if s.Rate == "send" {
			sendBucket++
		}
	}
	if sendBucket != 6 {
		t.Fatalf("%d operations in the send bucket", sendBucket)
	}
}

// The hand-written P0 methods cover exactly these operations; everything else is generated.
func TestHandwrittenOperations(t *testing.T) {
	var got []string
	for _, op := range operationTable {
		if op.handwritten {
			got = append(got, op.id)
		}
	}
	sort.Strings(got)
	want := []string{
		"ackReportPolling", "getMessageHistory", "getMessageStatistics", "getMessageStatusByMsgKey",
		"getMessageStatusByRequestId", "getMoByMsgKey", "getMoHistory", "getReportInquiry", "getReportPolling",
		"sendOmni", "uploadMmsFile", "uploadRcsFile",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("hand-written: %v", got)
	}
	// every brand message upload path is a kind of Files.UploadBrandMessage, with its operation
	n := 0
	for _, op := range operationTable {
		if kind, ok := strings.CutPrefix(op.path, "/api/comm/v1/file/brandmessage/"); ok {
			n++
			if brandImageKinds[BrandImageKind(kind)] != op {
				t.Fatalf("kind %q", kind)
			}
		}
	}
	if n != len(brandImageKinds) {
		t.Fatalf("%d brand message upload operations, %d kinds", n, len(brandImageKinds))
	}
}

func TestWebhookTableMatchesTheSpec(t *testing.T) {
	spec := loadSpec(t).Webhooks
	if len(spec) != 9 || len(webhookTable) != len(spec) {
		t.Fatalf("%d webhooks", len(spec))
	}
	for i, w := range spec {
		got := webhookTable[i]
		if got.name != w.Name || got.id != w.ID || got.signed != w.Signed {
			t.Fatalf("%+v != %+v", got, w)
		}
		if got.handwritten != (w.Name == "report" || w.Name == "mo") {
			t.Fatalf("%s handwritten=%v", w.Name, got.handwritten)
		}
		if _, ok := specSchemas[w.Payload]; !ok {
			t.Fatalf("no model for %s", w.Payload)
		}
	}
}

func TestEverySchemaHasAModel(t *testing.T) {
	for _, name := range loadSpec(t).Schemas {
		if strings.HasSuffix(name, "FlowItem") && name != "MessageFlowItem" && name != "ReservationMessageFlowItem" {
			continue // flow items are fields of their union (MessageFlowItem, ReservationMessageFlowItem)
		}
		if _, ok := specSchemas[name]; !ok {
			t.Errorf("no Go model for schema %s", name)
		}
	}
}

func TestServiceCodeTableMatchesTheErrorCodeFile(t *testing.T) {
	raw, err := os.ReadFile("spec/error-codes.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Service []struct {
			Code          string
			HTTPStatus    int `json:"httpStatus"`
			DescriptionKo string
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Service) != len(serviceCodes) || errorCodesSource == "" {
		t.Fatalf("%d codes in the file, %d in the table", len(doc.Service), len(serviceCodes))
	}
	for _, e := range doc.Service {
		if got := serviceCodes[e.Code]; got.httpStatus != e.HTTPStatus || (e.DescriptionKo != "" && got.description != e.DescriptionKo) {
			t.Fatalf("%s: %+v", e.Code, got)
		}
	}
}

func TestEUCKRTable(t *testing.T) {
	n := 0
	for _, w := range cp949Bits {
		for ; w != 0; w &= w - 1 {
			n++
		}
	}
	if n != cp949Count || cp949Count != 128+17048 {
		t.Fatalf("%d characters", n)
	}
}

func TestVersion(t *testing.T) {
	if Version != "1.2.0" || sdkClient != "bizgo-sdk-comm-go/1.2.0" || !strings.HasPrefix(buildUserAgent(""), "bizgo-sdk-comm-go/1.2.0 go/") {
		t.Fatal(Version)
	}
	if loadSpec(t).SpecVersion == "" {
		t.Fatal("no spec version")
	}
}

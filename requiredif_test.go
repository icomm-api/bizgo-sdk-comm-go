package bizgo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// x-sdk-required-if (SDK-DESIGN §4): conditional required fields are checked before sending,
// from the tables generated out of the spec, and the errors name paths and conditions only.

func problemPaths(t *testing.T, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	var paths []string
	for _, p := range ve.Problems {
		paths = append(paths, p.Path)
	}
	return paths
}

func TestAlimtalkFullSendNeedsMsgTypeAndTextBeforeSending(t *testing.T) {
	c, srv, _ := newTestClient(t)
	route := srv.On("POST", sendRoute, accepted("A000"))
	const secret = "SECRET_VALUE_XYZ"
	_, err := c.Send.Omni(context.Background(), OmniParams{
		To:       []Destination{{To: phone, ReplaceWords: map[string]string{"name": secret}}},
		Messages: []ChannelMessage{&AlimtalkMessage{SenderKey: "SENDER_KEY_EXAMPLE", TemplateCode: "TEMPLATE_CODE_EXAMPLE", Title: secret}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(problemPaths(t, err), " "); got != "messageFlow[0].alimtalk.msgType messageFlow[0].alimtalk.text" {
		t.Fatalf("paths = %s", got)
	}
	mustContain(t, err.Error(), "sendType != template이면 필수입니다")
	mustNotContain(t, err.Error(), secret, phone, "SENDER_KEY_EXAMPLE", "TEMPLATE_CODE_EXAMPLE")
	if route.Count() != 0 {
		t.Fatal("request was sent")
	}

	_, err = c.Send.Omni(context.Background(), OmniParams{
		To:       To(phone),
		Messages: []ChannelMessage{&AlimtalkMessage{SenderKey: "S", TemplateCode: "T", MsgType: "AT", Text: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.Count() != 1 {
		t.Fatalf("sent %d requests", route.Count())
	}
}

func TestAlimtalkTemplateSendNeedsReplaceWordsOfEveryDestination(t *testing.T) {
	msg := func() MessageFlowItem {
		return MessageFlowItem{Alimtalk: &AlimtalkMessage{SenderKey: "S", TemplateCode: "T", SendType: "template"}}
	}
	req := &SendOmniRequest{
		Destinations: []Destination{{To: phone, ReplaceWords: map[string]string{"name": "v"}}, {To: phone}, {To: phone}},
		MessageFlow:  []MessageFlowItem{msg(), msg()}, // two messages report each destination once
	}
	err := req.Validate()
	if got := strings.Join(problemPaths(t, err), " "); got != "destinations[1].replaceWords destinations[2].replaceWords" {
		t.Fatalf("paths = %s (%v)", got, err)
	}
	mustContain(t, err.Error(), "messageFlow[0].alimtalk.sendType == template이면 필수입니다")

	for i := range req.Destinations {
		req.Destinations[i].ReplaceWords = map[string]string{"name": "v"}
	}
	if err := req.Validate(); err != nil { // msgType and text are not required for a template send
		t.Fatal(err)
	}

	// The same rules apply to a body parsed from JSON.
	_, err = ParseSendOmniRequest([]byte(`{"destinations":[{"to":"01000000000"}],
		"messageFlow":[{"alimtalk":{"senderKey":"S","templateCode":"T","sendType":"template"}}]}`))
	if got := strings.Join(problemPaths(t, err), " "); got != "destinations[0].replaceWords" {
		t.Fatalf("paths = %s", got)
	}
}

func TestRequiredIfOtherRules(t *testing.T) {
	cases := []struct {
		name string
		req  interface{ Validate() error }
		want string // problem paths; "" = valid
	}{
		{"brand button WL", &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{BrandMessage: &BrandMessage{
			SendType: "free", MsgType: "FT", SenderKey: "S", Text: "x",
			Attachment: &BrandMessageAttachment{Button: []BrandMessageButton{{Type: "WL", URLMobile: "https://example.com"}, {Type: "BK", Name: "b"}}},
		}}}}, "messageFlow[0].brandmessage.attachment.button[0].urlPc"},
		{"brand button WL complete", &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{BrandMessage: &BrandMessage{
			SendType: "free", MsgType: "FT", SenderKey: "S", Text: "x",
			Attachment: &BrandMessageAttachment{Button: []BrandMessageButton{{Type: "WL", URLMobile: "https://example.com", URLPc: "https://example.com"}}},
		}}}}, ""},
		{"brand basic", &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{BrandMessage: &BrandMessage{
			SendType: "basic", SenderKey: "S",
		}}}}, "messageFlow[0].brandmessage.msgType messageFlow[0].brandmessage.templateCode messageFlow[0].brandmessage.targeting"},
		{"rcs header 1", &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{RCS: &RCSMessage{
			From: phone, FormatID: "F", BrandKey: "B", Body: &RCSBody{Description: "d"}, Header: "1",
		}}}}, "messageFlow[0].rcs.footer"},
		{"rcs header 0", &SendOmniRequest{Destinations: To(phone), MessageFlow: []MessageFlowItem{{RCS: &RCSMessage{
			From: phone, FormatID: "F", BrandKey: "B", Body: &RCSBody{Description: "d"}, Header: "0",
		}}}}, ""},
		{"counsel FILE without attachment", &CounselPlainMessageRequest{UserKey: "USER_KEY_EXAMPLE", SenderKey: "S", MsgType: "FILE", Message: "m"},
			"attachment.file.fileName attachment.file.fileSize"},
		{"counsel FILE", &CounselPlainMessageRequest{UserKey: "USER_KEY_EXAMPLE", SenderKey: "S", MsgType: "FILE", Message: "m",
			Attachment: &CounselPlainAttachment{File: &CounselFile{FileURL: "https://example.com/f", FileName: "f.pdf"}}},
			"attachment.file.fileSize"},
	}
	for _, tc := range cases {
		err := tc.req.Validate()
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if got := problemPaths(t, err); strings.Join(got, " ") != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, err, tc.want)
		}
		mustNotContain(t, err.Error(), phone, "example.com", "USER_KEY_EXAMPLE")
	}
}

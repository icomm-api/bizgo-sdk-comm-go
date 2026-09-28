// Command send-mms uploads an image, then sends an MMS with it.
//
//	BIZGO_API_KEY=... BIZGO_FROM=... BIZGO_TO=... BIZGO_IMAGE=banner.jpg go run ./examples/send-mms
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/icomm-api/bizgo-sdk-comm-go"
)

func run(ctx context.Context, client *bizgo.Client, from, to string, image io.Reader, out io.Writer) error {
	uploaded, err := client.Files.UploadMMS(ctx, bizgo.UploadParams{File: image, Filename: "banner.jpg"}) // jpg, max 300KB
	if err != nil {
		return err
	}
	if uploaded.FileKey == "" {
		return errors.New("업로드 응답에 fileKey가 없습니다")
	}
	result, err := client.Send.MMS(ctx, bizgo.MMSParams{
		To: bizgo.To(to), From: from, Title: "이벤트 안내", Text: "첨부 이미지를 확인해 주세요.",
		FileKeys: []string{uploaded.FileKey},
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "접수:", result.MsgKeys(), "파일 키 만료:", uploaded.Expired)
	return nil
}

func main() {
	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox))
	if err != nil {
		log.Fatal(err)
	}
	f, err := os.Open(env("BIZGO_IMAGE"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := run(context.Background(), client, env("BIZGO_FROM"), env("BIZGO_TO"), f, os.Stdout); err != nil {
		log.Print(err)
	}
}

func env(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("환경변수 %s를 설정하세요 (examples/README.md 참고)", name)
	}
	return v
}

package bizgo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
)

// MMSMaxBytes is the largest MMS image (300KB). Larger files are rejected before sending.
const MMSMaxBytes = 300 * 1024

// maxUploadBytes bounds how much of a reader is buffered for the other upload types.
const maxUploadBytes = 10 << 20

// BrandImageKind is the brand message image type. Each has its own size and ratio rules (see
// the API reference).
type BrandImageKind string

// Brand message image kinds. No other value is accepted.
const (
	BrandImageDefault           BrandImageKind = "default"
	BrandImageWide              BrandImageKind = "wide"
	BrandImageWideItemList      BrandImageKind = "wideItemList"
	BrandImageWideItemListFirst BrandImageKind = "wideItemList/first"
	BrandImageCarouselFeed      BrandImageKind = "carouselFeed"
	BrandImageCarouselCommerce  BrandImageKind = "carouselCommerce"
	BrandImageCatalog           BrandImageKind = "catalog"
	BrandImageCatalogOddFirst   BrandImageKind = "catalog/oddFirst"
)

// brandImageKinds maps each kind to its operation.
var brandImageKinds = map[BrandImageKind]*operation{
	BrandImageDefault:           opUploadBrandMessageDefaultImage,
	BrandImageWide:              opUploadBrandMessageWideImage,
	BrandImageWideItemList:      opUploadBrandMessageWideItemListImage,
	BrandImageWideItemListFirst: opUploadBrandMessageWideItemListFirstImage,
	BrandImageCarouselFeed:      opUploadBrandMessageCarouselFeedImage,
	BrandImageCarouselCommerce:  opUploadBrandMessageCarouselCommerceImage,
	BrandImageCatalog:           opUploadBrandMessageCatalogImage,
	BrandImageCatalogOddFirst:   opUploadBrandMessageCatalogOddFirstImage,
}

// FilesService uploads images. Use it as client.Files. Uploads are retried only on HTTP 429.
type FilesService struct{ t *transport }

// UploadParams describe one file to upload.
type UploadParams struct {
	// File is read once (so retries can resend it) and is not closed.
	File io.Reader
	// Filename is sent in the multipart part. Default: the base name of an *os.File, or "image.jpg".
	Filename string
	// FileKey is an optional key of your own for the file.
	FileKey string
	// ImageName is an optional image name.
	ImageName string
}

// UploadMMS uploads an MMS image (jpg, max 300KB). Use the returned FileKey in
// MMSMessage.FileKey (max 3 keys).
func (s *FilesService) UploadMMS(ctx context.Context, p UploadParams) (*FileUploadResult, error) {
	form, err := buildForm(p, MMSMaxBytes, "MMS 이미지는 최대 300KB입니다(jpg, 권장 1,500×1,440px 이하)")
	if err != nil {
		return nil, err
	}
	resp, err := call[FileUploadResponse](ctx, s.tr(), form.request(opUploadMMSFile))
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || resp.Data.Data == nil {
		return &FileUploadResult{}, nil
	}
	return resp.Data.Data, nil
}

// UploadRCS uploads an RCS image. Use the returned Media in the RCS message body.
func (s *FilesService) UploadRCS(ctx context.Context, p UploadParams) (*RCSFileUploadResult, error) {
	form, err := buildForm(p, maxUploadBytes, "파일이 너무 큽니다")
	if err != nil {
		return nil, err
	}
	resp, err := call[RCSFileUploadResponse](ctx, s.tr(), form.request(opUploadRCSFile))
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || resp.Data.Data == nil {
		return &RCSFileUploadResult{}, nil
	}
	return resp.Data.Data, nil
}

// UploadBrandMessage uploads a Kakao brand message image of the given kind. Use the returned
// ImgURL in the message. (The generated UploadBrandMessageDefault, UploadBrandMessageWide, ... do
// the same for one kind each.)
func (s *FilesService) UploadBrandMessage(ctx context.Context, kind BrandImageKind, p UploadParams) (*BrandMessageFileUploadResult, error) {
	op, ok := brandImageKinds[kind]
	if !ok {
		return nil, invalid("kind", "지원하지 않는 브랜드메시지 이미지 종류입니다(default, wide, wideItemList, wideItemList/first, "+
			"carouselFeed, carouselCommerce, catalog, catalog/oddFirst)")
	}
	form, err := buildForm(p, maxUploadBytes, "파일이 너무 큽니다")
	if err != nil {
		return nil, err
	}
	resp, err := call[BrandMessageFileUploadResponse](ctx, s.tr(), form.request(op))
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || resp.Data.Data == nil {
		return &BrandMessageFileUploadResult{}, nil
	}
	return resp.Data.Data, nil
}

type form struct {
	body        []byte
	contentType string
}

func (f form) request(op *operation) request {
	return request{op: op, method: op.method, path: op.path, body: f.body, contentType: f.contentType, policy: op.retry}
}

var extTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".bmp": "image/bmp",
	".pdf": "application/pdf", ".txt": "text/plain", ".csv": "text/csv", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".mp4": "video/mp4", ".mp3": "audio/mpeg", ".m4a": "audio/mp4",
}

// detectContentType uses a fixed extension table (the OS MIME registry differs between machines),
// then image content sniffing, then application/octet-stream.
func detectContentType(name string, content []byte) string {
	if t, ok := extTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	if t := http.DetectContentType(content); strings.HasPrefix(t, "image/") {
		return t
	}
	return "application/octet-stream"
}

// headerSafe removes characters that could break out of a multipart header.
func headerSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func buildForm(p UploadParams, limit int64, tooLarge string) (form, error) {
	if p.File == nil {
		return form{}, invalid("file", "파일이 없습니다")
	}
	name := p.Filename
	if name == "" {
		if named, ok := p.File.(interface{ Name() string }); ok {
			name = baseName(named.Name())
		}
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "image.jpg"
	}
	content, err := io.ReadAll(io.LimitReader(p.File, limit+1))
	if err != nil {
		// never the error itself: an *fs.PathError contains the full path
		return form{}, invalid("file", fileProblem(err)+": "+name)
	}
	if int64(len(content)) > limit {
		return form{}, invalid("file", tooLarge)
	}
	if len(content) == 0 {
		return form{}, invalid("file", "빈 파일입니다")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, headerSafe(name)))
	h.Set("Content-Type", detectContentType(name, content))
	part, err := w.CreatePart(h)
	if err != nil {
		return form{}, err
	}
	if _, err := part.Write(content); err != nil {
		return form{}, err
	}
	for _, f := range []struct{ name, value string }{{"fileKey", p.FileKey}, {"imageName", p.ImageName}} {
		if f.value == "" {
			continue
		}
		if err := w.WriteField(f.name, f.value); err != nil {
			return form{}, err
		}
	}
	if err := w.Close(); err != nil {
		return form{}, err
	}
	return form{body: buf.Bytes(), contentType: w.FormDataContentType()}, nil
}

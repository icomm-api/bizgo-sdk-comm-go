package bizgo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Runtime helpers of the methods generated from the spec (services_gen.go).

// maxFormFileBytes bounds how much of one file of a generated multipart operation is buffered (the
// body is kept in memory so that a retried request resends the same bytes).
const maxFormFileBytes = 100 << 20

// buildPath fills the {placeholders} of a path template, in order, with escaped values.
func buildPath(template string, values ...string) (string, error) {
	var b strings.Builder
	rest := template
	for _, value := range values {
		i := strings.IndexByte(rest, '{')
		j := strings.IndexByte(rest, '}')
		if i < 0 || j < i {
			return "", errors.New("bizgo: path template has fewer placeholders than values")
		}
		seg, err := segment(rest[i+1:j], value)
		if err != nil {
			return "", err
		}
		b.WriteString(rest[:i])
		b.WriteString(seg)
		rest = rest[j+1:]
	}
	b.WriteString(rest)
	return b.String(), nil
}

func formatInt[T int | int64](v T) string { return strconv.FormatInt(int64(v), 10) }

//nolint:unused // emitted by tools/gen for boolean query parameters (none in the spec yet)
func formatBool(v bool) string { return strconv.FormatBool(v) }

func joinComma(values []string) string { return strings.Join(values, ",") }

// advanceText moves a string cursor; false means there is no next page.
func advanceText(cursor *string, hasNext bool, next string) bool {
	if !hasNext || next == "" || next == *cursor {
		return false
	}
	*cursor = next
	return true
}

// reachedTextTotal reports whether seen reached a total sent as a string ("" or not a number: no).
func reachedTextTotal(seen int, total string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(total))
	return err == nil && seen >= n
}

func (v *validator) minLength(path, s string, limit int) {
	if n := utf8.RuneCountInString(s); n < limit {
		v.add(path, fmt.Sprintf("최소 %d자입니다", limit))
	}
}

// header checks a header parameter: one line of printable ASCII (no CR, LF or other control characters).
func (v *validator) header(path, s string) {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			v.add(path, "헤더 값에는 출력 가능한 ASCII 문자만 쓸 수 있습니다(줄바꿈·제어문자·한글 불가)")
			return
		}
	}
}

// defaultIdempotencyTTL returns the idempotencyTtl to send with key: [DefaultIdempotencyTTL] when a
// key is set without a TTL (the API rejects that with A309), otherwise ttl unchanged (an explicit
// value, 0 included, is kept; without a key nothing is added). ok reports whether it was filled.
func defaultIdempotencyTTL(key string, ttl *int) (_ *int, ok bool) {
	if key == "" || ttl != nil {
		return ttl, false
	}
	v := DefaultIdempotencyTTL
	return &v, true
}

// buildJSON serializes a validated request body.
func buildJSON(body any) (form, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return form{}, invalid("", "요청을 JSON으로 만들 수 없습니다")
	}
	return form{body: data, contentType: "application/json"}, nil
}

// multipartBody is a generated multipart request model.
type multipartBody interface {
	writeForm(f *formWriter)
}

// buildMultipart reads the files of a validated multipart model (once) and builds the body.
func buildMultipart(body multipartBody) (form, error) {
	var buf bytes.Buffer
	f := &formWriter{w: multipart.NewWriter(&buf)}
	body.writeForm(f)
	if f.err != nil {
		return form{}, f.err
	}
	if err := f.w.Close(); err != nil {
		return form{}, err
	}
	return form{body: buf.Bytes(), contentType: f.w.FormDataContentType()}, nil
}

// formWriter writes multipart fields and keeps the first error.
type formWriter struct {
	w   *multipart.Writer
	err error
}

func (f *formWriter) text(name, value string) {
	if f.err != nil || value == "" {
		return
	}
	f.err = f.w.WriteField(name, value)
}

//nolint:unused // emitted by tools/gen for number and boolean multipart fields (none in the spec yet)
func (f *formWriter) value(name string, value any) {
	if f.err != nil {
		return
	}
	f.err = f.w.WriteField(name, fmt.Sprint(value))
}

// json writes an application/json part (encoding contentType application/json in the spec).
func (f *formWriter) json(name string, value any) {
	if f.err != nil || isNil(value) {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		f.err = invalid(name, "JSON으로 만들 수 없습니다")
		return
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, headerSafe(name)))
	h.Set("Content-Type", "application/json")
	part, err := f.w.CreatePart(h)
	if err != nil {
		f.err = err
		return
	}
	_, f.err = part.Write(data)
}

func (f *formWriter) file(name string, file *UploadFile) {
	if f.err != nil || file == nil {
		return
	}
	filename, content, err := file.read(name, maxFormFileBytes)
	if err != nil {
		f.err = err
		return
	}
	contentType := file.ContentType
	if contentType == "" {
		contentType = detectContentType(filename, content)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, headerSafe(name), headerSafe(filename)))
	h.Set("Content-Type", headerSafe(contentType))
	part, err := f.w.CreatePart(h)
	if err != nil {
		f.err = err
		return
	}
	_, f.err = part.Write(content)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// UploadFile is one file of a multipart operation (templates, brand and chatbot updates, counsel
// files, ...). Set Reader or Path.
//
// The file is read once, before the first attempt, so that retries resend the same bytes; at most
// 100MB per file are buffered. A file that cannot be opened or read is a [*ValidationError] that
// names the file (base name only, never the full path).
type UploadFile struct {
	// Reader is read to the end (not closed).
	Reader io.Reader
	// Path is opened, read and closed when Reader is nil.
	Path string
	// Filename is sent in the part. Default: the base name of Path (or of an *os.File), or "file".
	Filename string
	// ContentType of the part. Default: from the extension, then from the content, else
	// application/octet-stream.
	ContentType string
}

// String describes the file without its path or content.
func (u UploadFile) String() string { return "bizgo.UploadFile(" + u.name() + ")" }

func (u *UploadFile) name() string {
	if u.Filename != "" {
		return u.Filename
	}
	if u.Path != "" {
		return filepath.Base(u.Path)
	}
	if named, ok := u.Reader.(interface{ Name() string }); ok {
		return filepath.Base(named.Name())
	}
	return "file"
}

func (u *UploadFile) validate(v *validator, path string) {
	if u.Reader == nil && u.Path == "" {
		v.add(path, "파일이 없습니다(Reader 또는 Path를 설정하세요)")
	}
}

// read returns the file name and content. Errors never contain the full path.
func (u *UploadFile) read(field string, limit int64) (string, []byte, error) {
	name := u.name()
	r := u.Reader
	if r == nil {
		f, err := os.Open(u.Path)
		if err != nil {
			return "", nil, invalid(field, fileProblem(err)+": "+filepath.Base(u.Path))
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	content, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return "", nil, invalid(field, fileProblem(err)+": "+name)
	}
	if int64(len(content)) > limit {
		return "", nil, invalid(field, fmt.Sprintf("파일이 너무 큽니다(최대 %dMB): %s", limit>>20, name))
	}
	return name, content, nil
}

func fileProblem(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "파일이 없습니다"
	case errors.Is(err, fs.ErrPermission):
		return "파일을 읽을 권한이 없습니다"
	}
	return "파일을 읽지 못했습니다"
}

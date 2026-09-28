package bizgo

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// String() of the models (SDK-DESIGN.md §12.11): phone numbers are masked (010****0000) and message
// content is shown as its length, so that logging a model with %v does not leak them. The field
// values themselves are unchanged.

// MaskPhone masks the middle of a phone number (SDK-DESIGN.md §12.11):
//
//	"01000001234" -> "010****1234"  (11 characters or more: first 3 and last 4 kept)
//	"15880000"    -> "158****0"     (8-10 characters: first 3 kept, at least half masked)
//	"1234567"     -> "*******"      (7 characters or fewer: all masked)
func MaskPhone(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	n := len(r)
	switch {
	case n <= 7:
		return strings.Repeat("*", n)
	case n >= 11:
		return string(r[:3]) + strings.Repeat("*", n-7) + string(r[n-4:])
	}
	masked := (n + 1) / 2
	return string(r[:3]) + strings.Repeat("*", masked) + string(r[3+masked:])
}

type printer struct {
	b     strings.Builder
	first bool
}

func newPrinter(name string) *printer {
	p := &printer{first: true}
	p.b.WriteString(name)
	p.b.WriteByte('{')
	return p
}

func (p *printer) sep(name string) {
	if !p.first {
		p.b.WriteString(", ")
	}
	p.first = false
	p.b.WriteString(name)
	p.b.WriteByte(':')
}

func (p *printer) phone(name, v string) {
	if v != "" {
		p.sep(name)
		p.b.WriteString(MaskPhone(v))
	}
}

func (p *printer) phones(name string, v []string) {
	if len(v) == 0 {
		return
	}
	masked := make([]string, len(v))
	for i, s := range v {
		masked[i] = MaskPhone(s)
	}
	p.sep(name)
	p.b.WriteString("[" + strings.Join(masked, " ") + "]")
}

func (p *printer) content(name, v string) {
	if v != "" {
		p.sep(name)
		p.b.WriteString("(" + strconv.Itoa(utf8.RuneCountInString(v)) + "자)")
	}
}

// person shows the first character of a person's name or e-mail address only.
func (p *printer) person(name, v string) {
	if v == "" {
		return
	}
	r := []rune(v)
	p.sep(name)
	p.b.WriteString(string(r[0]) + strings.Repeat("*", len(r)-1))
}

// secret hides a token, key or authentication number completely.
func (p *printer) secret(name string, present bool) {
	if present {
		p.sep(name)
		p.b.WriteString("[REDACTED]")
	}
}

func (p *printer) file(name string, present bool) {
	if present {
		p.sep(name)
		p.b.WriteString("(file)")
	}
}

func (p *printer) keys(name string, n int) {
	if n > 0 {
		p.sep(name)
		p.b.WriteString("(" + strconv.Itoa(n) + " fields)")
	}
}

// value prints a non-zero value; pointers to scalars are dereferenced, models print their own String.
func (p *printer) value(name string, v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsZero() {
		return
	}
	if rv.Kind() == reflect.Pointer && rv.Elem().Kind() != reflect.Struct {
		v = rv.Elem().Interface()
	}
	p.sep(name)
	fmt.Fprint(&p.b, v)
}

func (p *printer) String() string {
	p.b.WriteByte('}')
	return p.b.String()
}

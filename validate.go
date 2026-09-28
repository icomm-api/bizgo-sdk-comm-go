package bizgo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FieldProblem is one reason a request was rejected before sending.
type FieldProblem struct {
	Path   string // API field path, for example "messageFlow[0].sms.text"
	Reason string // why, in Korean; never contains the field's value
}

// ValidationError means the request was rejected before anything was sent.
//
// It lists field paths and reasons only. Input values (which can be phone numbers) are never
// included.
type ValidationError struct {
	Problems []FieldProblem
}

const maxProblemsInMessage = 10

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("요청이 올바르지 않습니다(보내지 않음): ")
	for i, p := range e.Problems {
		if i == maxProblemsInMessage {
			fmt.Fprintf(&b, "; 외 %d건", len(e.Problems)-i)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		if p.Path != "" {
			b.WriteString(p.Path + ": ")
		}
		b.WriteString(p.Reason)
	}
	return b.String()
}

// Is matches ErrValidation.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

func invalid(path, reason string) error {
	return &ValidationError{Problems: []FieldProblem{{Path: path, Reason: reason}}}
}

type validator struct {
	problems []FieldProblem
}

func (v *validator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}

func (v *validator) add(path, reason string) {
	v.problems = append(v.problems, FieldProblem{Path: path, Reason: reason})
}

func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

func indexPath(base string, i int) string {
	return base + "[" + strconv.Itoa(i) + "]"
}

func (v *validator) required(path string) { v.add(path, "필수 필드입니다") }

func (v *validator) maxLength(path, s string, limit int) {
	if n := utf8.RuneCountInString(s); n > limit {
		v.add(path, fmt.Sprintf("최대 %d자인데 %d자입니다", limit, n))
	}
}

//nolint:unused // emitted by tools/gen for x-max-bytes fields that are not EUC-KR
func (v *validator) maxBytesUTF8(path, s string, limit int) {
	if n := len(s); n > limit {
		v.add(path, fmt.Sprintf("최대 %dbyte인데 %dbyte입니다", limit, n))
	}
}

func (v *validator) maxBytesEUCKR(path, s string, limit int) {
	n, err := EUCKRLen(s)
	if err != nil {
		v.add(path, err.Error())
		return
	}
	if n > limit {
		v.add(path, fmt.Sprintf("최대 %dbyte인데 %dbyte입니다(EUC-KR 기준)", limit, n))
	}
}

func (v *validator) pattern(path, s string, re *regexp.Regexp) {
	if !re.MatchString(s) {
		v.add(path, "형식이 맞지 않습니다("+re.String()+")")
	}
}

func (v *validator) enum(path, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	v.add(path, "허용값은 "+strings.Join(allowed, ", ")+" 중 하나입니다")
}

func (v *validator) items(path string, n int, lo, hi int) {
	if lo >= 0 && n < lo {
		v.add(path, fmt.Sprintf("최소 %d개인데 %d개입니다", lo, n))
	}
	if hi >= 0 && n > hi {
		v.add(path, fmt.Sprintf("최대 %d개인데 %d개입니다", hi, n))
	}
}

func (v *validator) min(path string, x, lo float64) {
	if x < lo {
		v.add(path, "최솟값은 "+strconv.FormatFloat(lo, 'f', -1, 64)+"입니다")
	}
}

func (v *validator) max(path string, x, hi float64) {
	if x > hi {
		v.add(path, "최댓값은 "+strconv.FormatFloat(hi, 'f', -1, 64)+"입니다")
	}
}

var errNotEUCKR = errors.New("EUC-KR로 표현할 수 없는 문자가 있습니다(이모지 등은 문자메시지에 쓸 수 없습니다)")

// EUCKRLen returns the length of s in bytes as the carriers count it: encoded with CP949 (the
// superset of EUC-KR with the same 2 bytes per Hangul syllable). ASCII is 1 byte, every other
// supported character 2 bytes. It returns an error, without the offending text, when s has a
// character that CP949 cannot encode, such as an emoji, or invalid UTF-8.
func EUCKRLen(s string) (int, error) {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r < utf8.RuneSelf:
			n++
		case r == utf8.RuneError && size == 1, r > 0xFFFF, cp949Bits[r/64]&(1<<(uint(r)%64)) == 0:
			return 0, errNotEUCKR
		default:
			n += 2
		}
		i += size
	}
	return n, nil
}

// requiredIfRule is one x-sdk-required-if rule of the spec (generated tables in models_gen.go):
// when the object's field compares true with op and values, every path must be present
// (a key with a non-null value). Values are compared as strings; a missing or null field makes
// "==" and "in" false, "!=" and "not in" true.
type requiredIfRule struct {
	field     string
	op        string // "==", "!=", "in", "not in"
	values    []string
	paths     []string // relative to the object: "msgType", "attachment.file.fileName", "arr[].x"
	rootPaths []string // from the request body root ("$." in the spec): "destinations[].replaceWords"
}

// requiredIfSite is where objects with rules occur in a request body, from its root:
// "messageFlow[].alimtalk" ("[]" is every element, "" the root itself).
type requiredIfSite struct {
	at    string
	rules []requiredIfRule
}

// requiredIf checks the conditional required fields of a request body. It evaluates the rules on
// the JSON form of body, the same bytes the API receives, so it sees exactly what is sent (an empty
// optional string is omitted, hence missing). Problems name paths and the spec condition only.
func (v *validator) requiredIf(base string, body any, sites []requiredIfSite) {
	raw, err := json.Marshal(body)
	if err != nil {
		return // reported when the body is serialized for sending
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&root) != nil {
		return
	}
	seen := map[[2]string]bool{} // path, condition: a root path is reported once for all objects
	report := func(path, cond, reason string) {
		if k := [2]string{path, cond}; !seen[k] {
			seen[k] = true
			v.add(path, reason)
		}
	}
	for _, site := range sites {
		var objects []jsonAt
		collectJSON(root, base, splitRulePath(site.at), &objects, nil)
		for _, obj := range objects {
			m, ok := obj.value.(map[string]any)
			if !ok {
				continue
			}
			for _, r := range site.rules {
				if !r.applies(m) {
					continue
				}
				cond := r.condition()
				reason := cond + "이면 필수입니다"
				for _, p := range r.paths {
					for _, miss := range missingPaths(m, obj.path, p) {
						report(miss, cond, reason)
					}
				}
				for _, p := range r.rootPaths {
					for _, miss := range missingPaths(root, base, p) {
						report(miss, cond, joinPath(obj.path, reason))
					}
				}
			}
		}
	}
}

func (r requiredIfRule) applies(m map[string]any) bool {
	s, present := jsonScalar(m[r.field])
	match := present && slices.Contains(r.values, s)
	switch r.op {
	case "==", "in":
		return match
	case "!=", "not in":
		return !match
	}
	return false
}

// condition is the rule's condition with the spec's values (never the request's).
func (r requiredIfRule) condition() string {
	if r.op == "in" || r.op == "not in" {
		return r.field + " " + r.op + " [" + strings.Join(r.values, ", ") + "]"
	}
	return r.field + " " + r.op + " " + r.values[0]
}

// jsonScalar is a decoded JSON scalar as a string; false for null, missing, objects and arrays.
func jsonScalar(x any) (string, bool) {
	switch t := x.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

type jsonAt struct {
	path  string
	value any
}

func splitRulePath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// collectJSON follows segs ("name" or "name[]") from x and appends what it reaches. With missing
// set, a key that is absent or null is reported there instead (with the rest of the path).
func collectJSON(x any, path string, segs []string, out *[]jsonAt, missing *[]string) {
	if len(segs) == 0 {
		*out = append(*out, jsonAt{path, x})
		return
	}
	name, each := strings.CutSuffix(segs[0], "[]")
	m, ok := x.(map[string]any)
	if !ok {
		return // not an object: a type problem, not a missing field
	}
	child, ok := m[name]
	if !ok || child == nil {
		if missing != nil {
			*missing = append(*missing, withRest(joinPath(path, name), segs[1:]))
		}
		return
	}
	p := joinPath(path, name)
	if !each {
		collectJSON(child, p, segs[1:], out, missing)
		return
	}
	arr, _ := child.([]any)
	for i, item := range arr { // no elements: nothing to check
		ip := indexPath(p, i)
		if item == nil {
			if missing != nil {
				*missing = append(*missing, withRest(ip, segs[1:]))
			}
			continue
		}
		collectJSON(item, ip, segs[1:], out, missing)
	}
}

// missingPaths returns the concrete paths of p under x (at path) that are absent or null.
func missingPaths(x any, path, p string) []string {
	var missing []string
	var reached []jsonAt
	collectJSON(x, path, splitRulePath(p), &reached, &missing)
	return missing
}

// withRest appends the unreached segments to path, for reporting a missing field.
func withRest(path string, rest []string) string {
	if len(rest) == 0 {
		return path
	}
	return joinPath(path, strings.Join(rest, "."))
}

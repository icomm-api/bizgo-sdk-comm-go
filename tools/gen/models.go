package main

import (
	"fmt"
	"go/format"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

const refPrefix = "#/components/schemas/"

// primaryFlow is the channel union of the send API. Its members keep the method name flowItem()
// (used by the hand-written SendService); other unions get as<Union>().
const primaryFlow = "MessageFlowItem"

// Keywords the generator understands. Any other keyword in a request schema is an error, so that
// no constraint of the spec is silently dropped.
var knownKeywords = map[string]bool{
	"type": true, "description": true, "example": true, "examples": true, "properties": true,
	"required": true, "items": true, "$ref": true, "maxLength": true, "minLength": true, "minItems": true,
	"maxItems": true, "minimum": true, "maximum": true, "pattern": true, "enum": true, "default": true,
	"x-max-bytes": true, "x-charset": true, "x-format": true, "x-known-values": true, "x-unverified": true,
	"x-verified": true, "x-sdk-required-if": true, "additionalProperties": true, "title": true, "format": true, "allOf": true, "oneOf": true,
}

// Go initialisms applied word by word to spec names.
var initialisms = map[string]string{
	"id": "ID", "url": "URL", "ttl": "TTL", "sms": "SMS", "mms": "MMS", "lms": "LMS", "rcs": "RCS",
	"mo": "MO", "cid": "CID", "api": "API", "json": "JSON",
}

// Field names for the channel keys of flow unions.
var flowFieldNames = map[string]string{
	"sms": "SMS", "mms": "MMS", "international": "International", "rcs": "RCS",
	"alimtalk": "Alimtalk", "brandmessage": "BrandMessage", "navertalk": "NaverTalk",
}

// Fields whose values are phone numbers: String() shows them masked (010****0000).
var phoneFields = map[string]bool{
	"to": true, "from": true, "phoneNumber": true, "phone_number": true, "phoneNumbers": true,
	"unsubscribePhoneNumber": true, "originCID": true, "originator": true, "callback": true,
	"telNumber": true, "tel": true, "mdn": true,
}

// Fields with message content: String() shows only their length.
var contentFields = map[string]bool{
	"content": true, "text": true, "message": true, "comment": true, "additionalContent": true,
	"systemMessage": true, "title": true, "reportText": true,
}

// Fields that name a person: String() shows the first character only.
var personFields = map[string]bool{
	"userName": true, "nickname": true, "nickName": true, "email": true, "userEmail": true,
}

// Fields with secrets (tokens, authentication numbers, encrypted identity data): String() hides them.
var secretFields = map[string]bool{
	"token": true, "unsubscribeAuthNumber": true, "certResult": true, "apiKey": true, "secret": true,
	"password": true, "accessToken": true,
}

// maskCall is the printer call for one field (SDK-DESIGN.md §12.11), shared by models and params
// structs. inPersonalInfo: "name" names a person inside a personal-info object.
func maskCall(p, name, x string, t typ, inPersonalInfo bool) string {
	isString := t.kind == kString
	isStrings := t.kind == kArray && t.elem != nil && t.elem.kind == kString
	switch {
	case t.kind == kFile:
		return fmt.Sprintf("%s.file(%q, %s != nil)", p, name, x)
	case t.kind == kArray && t.elem != nil && t.elem.kind == kFile:
		return fmt.Sprintf("%s.file(%q, len(%s) > 0)", p, name, x)
	case phoneFields[name] && isString:
		return fmt.Sprintf("%s.phone(%q, %s)", p, name, x)
	case phoneFields[name] && isStrings:
		return fmt.Sprintf("%s.phones(%q, %s)", p, name, x)
	case secretFields[name] && (isString || isStrings):
		return fmt.Sprintf("%s.secret(%q, len(%s) > 0)", p, name, x)
	case (personFields[name] || inPersonalInfo && name == "name") && isString:
		return fmt.Sprintf("%s.person(%q, %s)", p, name, x)
	case contentFields[name] && isString:
		return fmt.Sprintf("%s.content(%q, %s)", p, name, x)
	case t.kind == kAnyMap || t.kind == kStringMap || name == "replaceWords":
		return fmt.Sprintf("%s.keys(%q, len(%s))", p, name, x)
	}
	return fmt.Sprintf("%s.value(%q, %s)", p, name, x)
}

func splitWords(s string) []string {
	var words []string
	runes := []rune(s)
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(cur)
		acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && next != 0 && unicode.IsLower(next)
		digit := unicode.IsDigit(cur) != unicode.IsDigit(prev)
		if lowerToUpper || acronymEnd || digit || cur == '_' || cur == '-' || cur == '/' || cur == '.' {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	words = append(words, string(runes[start:]))
	out := words[:0]
	for _, w := range words {
		w = strings.Trim(w, "_-/.")
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func goName(s string) string {
	var b strings.Builder
	for _, w := range splitWords(s) {
		if up, ok := initialisms[strings.ToLower(w)]; ok {
			b.WriteString(up)
			continue
		}
		r := []rune(w)
		b.WriteString(strings.ToUpper(string(r[0])) + string(r[1:]))
	}
	return b.String()
}

// fieldName is the Go name of a property. "extra" becomes ExtraValue: Extra holds unknown fields.
func fieldName(s string) string {
	if n := goName(s); n != "Extra" {
		return n
	}
	return "ExtraValue"
}

// lowerName is goName with a lower-case first word, for parameters and unexported names.
func lowerName(s string) string {
	n := goName(s)
	words := splitWords(n)
	if len(words) == 0 {
		return n
	}
	first := words[0]
	rest := strings.TrimPrefix(n, first)
	out := strings.ToLower(first) + rest
	if goKeywords[out] {
		out += "_"
	}
	return out
}

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true, "default": true, "defer": true,
	"else": true, "fallthrough": true, "for": true, "func": true, "go": true, "goto": true, "if": true,
	"import": true, "interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true, "ctx": true, "err": true,
	"s": true, "p": true, "body": true, "params": true,
}

type kind int

const (
	kString kind = iota
	kInt
	kInt64
	kFloat
	kBool
	kObject // named struct (pointer in fields)
	kArray
	kStringMap
	kAnyMap
	kFlow // a channel union (value in slices)
	kFile // multipart binary part
)

type typ struct {
	kind kind
	name string // Go type name for kObject/kFlow
	elem *typ   // for kArray
}

type field struct {
	json     string
	goName   string
	required bool
	t        typ
	schema   *yaml.Node
	path     string // for error messages
	jsonPart bool   // multipart: sent as an application/json part
}

type model struct {
	specName  string
	goName    string
	schema    *yaml.Node
	fields    []field
	request   bool // reachable from a request body: validate()
	response  bool // reachable from a response or webhook (or from nothing): Extra, UnmarshalJSON
	multipart bool // a multipart/form-data body: writeForm()
	flow      []flowMember
}

type flowMember struct {
	key, field, typeName string
}

type generator struct {
	schemas    *yaml.Node
	request    map[string]bool
	response   map[string]bool
	multipart  map[string]map[string]bool // schema -> properties sent as JSON parts
	models     []*model
	byGo       map[string]*model
	bySpec     map[string]*model
	patterns   []string
	unions     []*model
	roots      map[string]bool             // request body schemas: exported Validate()
	requiredIf map[string][]requiredIfSite // root schema -> x-sdk-required-if locations
	hand       map[string]map[string]bool  // hand-written types and their methods
}

func newGenerator(spec *yaml.Node) (*generator, error) {
	schemas := path(spec, "components", "schemas")
	if schemas == nil {
		return nil, fmt.Errorf("no components.schemas")
	}
	g := &generator{schemas: schemas, byGo: map[string]*model{}, bySpec: map[string]*model{}, multipart: map[string]map[string]bool{},
		roots: map[string]bool{}}
	var reqRoots, respRoots []string
	forEachOperation(spec, func(_, _ string, op *yaml.Node) {
		content := path(op, "requestBody", "content")
		for _, ct := range keys(content) {
			name := strings.TrimPrefix(str(path(content, ct, "schema", "$ref")), refPrefix)
			if name == "" {
				continue
			}
			reqRoots = append(reqRoots, name)
			g.roots[name] = true
			if ct == "multipart/form-data" {
				parts := map[string]bool{}
				enc := path(content, ct, "encoding")
				for _, p := range keys(enc) {
					if str(path(enc, p, "contentType")) == "application/json" {
						parts[p] = true
					}
				}
				g.multipart[name] = parts
			}
		}
		responses := get(op, "responses")
		for _, status := range keys(responses) {
			for _, ct := range keys(path(responses, status, "content")) {
				if name := strings.TrimPrefix(str(path(responses, status, "content", ct, "schema", "$ref")), refPrefix); name != "" {
					respRoots = append(respRoots, name)
				}
			}
		}
	})
	webhooks := get(spec, "webhooks")
	for _, name := range keys(webhooks) {
		op := path(webhooks, name, "post")
		if s := strings.TrimPrefix(str(path(op, "requestBody", "content", "application/json", "schema", "$ref")), refPrefix); s != "" {
			respRoots = append(respRoots, s) // webhook payloads are parsed like responses
		}
		if s := strings.TrimPrefix(str(path(op, "responses", "200", "content", "application/json", "schema", "$ref")), refPrefix); s != "" {
			respRoots = append(respRoots, s)
		}
	}
	g.request = closure(schemas, reqRoots)
	g.response = closure(schemas, respRoots)
	return g, nil
}

// parseModels builds the models of every schema.
func (g *generator) parseModels() error {
	flowMembers := map[string]bool{}
	for _, name := range keys(g.schemas) {
		if get(get(g.schemas, name), "oneOf") != nil {
			for _, m := range items(path(g.schemas, name, "oneOf")) {
				flowMembers[strings.TrimPrefix(str(get(m, "$ref")), refPrefix)] = true
			}
		}
	}
	for _, name := range keys(g.schemas) {
		if flowMembers[name] {
			continue // flow items are folded into their union
		}
		if err := g.addSchema(name); err != nil {
			return err
		}
	}
	return nil
}

// renderModels writes models_gen.go. Call it last: it also declares the patterns used by the services.
func (g *generator) renderModels() ([]byte, error) {
	g.requiredIf = map[string][]requiredIfSite{}
	for _, name := range keys(g.schemas) {
		if !g.roots[name] {
			continue
		}
		sites, err := g.requiredIfSites(name)
		if err != nil {
			return nil, err
		}
		g.requiredIf[name] = sites
	}
	src, err := g.render()
	if err != nil {
		return nil, err
	}
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("gofmt models: %w\n%s", err, src)
	}
	return out, nil
}

func refs(n *yaml.Node, found map[string]bool) {
	n = resolve(n)
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		if r := str(get(n, "$ref")); strings.HasPrefix(r, refPrefix) {
			found[strings.TrimPrefix(r, refPrefix)] = true
		}
	}
	for _, c := range n.Content {
		refs(c, found)
	}
}

func closure(schemas *yaml.Node, roots []string) map[string]bool {
	seen := map[string]bool{}
	stack := append([]string(nil), roots...)
	for len(stack) > 0 {
		name := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[name] {
			continue
		}
		seen[name] = true
		found := map[string]bool{}
		refs(get(schemas, name), found)
		for r := range found {
			if !seen[r] {
				stack = append(stack, r)
			}
		}
	}
	return seen
}

func (g *generator) addSchema(name string) error {
	s := get(g.schemas, name)
	m := &model{specName: name, goName: goName(name), schema: s, request: g.request[name]}
	m.response = g.response[name] || !m.request
	_, m.multipart = g.multipart[name]
	if get(s, "oneOf") != nil {
		for _, member := range items(get(s, "oneOf")) {
			memberName := strings.TrimPrefix(str(get(member, "$ref")), refPrefix)
			ms := get(g.schemas, memberName)
			req := strs(get(ms, "required"))
			props := keys(get(ms, "properties"))
			if len(req) != 1 || len(props) != 1 || req[0] != props[0] {
				return fmt.Errorf("%s: flow item must have exactly one required property", memberName)
			}
			key := req[0]
			target := strings.TrimPrefix(str(path(ms, "properties", key, "$ref")), refPrefix)
			fieldName, ok := flowFieldNames[key]
			if !ok {
				return fmt.Errorf("%s: no Go field name for channel key %q", memberName, key)
			}
			m.flow = append(m.flow, flowMember{key: key, field: fieldName, typeName: goName(target)})
		}
		g.unions = append(g.unions, m)
		return g.register(m)
	}
	if err := g.fillFields(m, s); err != nil {
		return err
	}
	return g.register(m)
}

func (g *generator) register(m *model) error {
	if _, dup := g.byGo[m.goName]; dup {
		return fmt.Errorf("duplicate Go type name %s", m.goName)
	}
	g.byGo[m.goName] = m
	g.bySpec[m.specName] = m
	g.models = append(g.models, m)
	return nil
}

// fillFields collects the properties of an object schema (following allOf).
func (g *generator) fillFields(m *model, s *yaml.Node) error {
	type propRef struct {
		name   string
		schema *yaml.Node
	}
	var props []propRef
	required := map[string]bool{}
	var collect func(s *yaml.Node) error
	collect = func(s *yaml.Node) error {
		if r := str(get(s, "$ref")); r != "" {
			return collect(get(g.schemas, strings.TrimPrefix(r, refPrefix)))
		}
		for _, part := range items(get(s, "allOf")) {
			if err := collect(part); err != nil {
				return err
			}
		}
		for _, r := range strs(get(s, "required")) {
			required[r] = true
		}
		p := get(s, "properties")
		for _, k := range keys(p) {
			idx := slices.IndexFunc(props, func(x propRef) bool { return x.name == k })
			if idx >= 0 {
				props[idx].schema = get(p, k) // later allOf parts override
				continue
			}
			props = append(props, propRef{k, get(p, k)})
		}
		return nil
	}
	if err := collect(s); err != nil {
		return err
	}
	jsonParts := g.multipart[m.specName]
	for _, p := range props {
		fpath := m.specName + "." + p.name
		if m.request {
			if err := checkKeywords(p.schema, fpath); err != nil {
				return err
			}
		}
		t, err := g.typeOf(p.schema, m, p.name, fpath)
		if err != nil {
			return err
		}
		if t.kind == kFile || (t.kind == kArray && t.elem.kind == kFile) {
			if !m.multipart {
				return fmt.Errorf("%s: binary field outside a multipart body", fpath)
			}
		}
		m.fields = append(m.fields, field{
			json: p.name, goName: fieldName(p.name), required: required[p.name], t: t, schema: p.schema, path: fpath,
			jsonPart: jsonParts[p.name],
		})
	}
	return nil
}

func checkKeywords(s *yaml.Node, where string) error {
	for _, k := range keys(s) {
		if !knownKeywords[k] {
			return fmt.Errorf("%s: unsupported keyword %q (teach tools/gen about it)", where, k)
		}
	}
	if it := get(s, "items"); it != nil {
		return checkKeywords(it, where+"[]")
	}
	return nil
}

// schemaType returns the JSON type of a schema, allowing ["x", "null"].
func schemaType(s *yaml.Node) string {
	t := get(s, "type")
	if t == nil {
		return ""
	}
	if t.Kind == yaml.SequenceNode {
		var non []string
		for _, v := range strs(t) {
			if v != "null" {
				non = append(non, v)
			}
		}
		if len(non) == 1 {
			return non[0]
		}
		return ""
	}
	return str(t)
}

func (g *generator) typeOf(s *yaml.Node, parent *model, prop, where string) (typ, error) {
	if r := str(get(s, "$ref")); r != "" {
		name := strings.TrimPrefix(r, refPrefix)
		if get(get(g.schemas, name), "oneOf") != nil {
			return typ{kind: kFlow, name: goName(name)}, nil
		}
		return typ{kind: kObject, name: goName(name)}, nil
	}
	if parts := items(get(s, "allOf")); len(parts) > 0 {
		if len(parts) == 1 && str(get(parts[0], "$ref")) != "" && get(s, "properties") == nil {
			return g.typeOf(parts[0], parent, prop, where)
		}
		return g.inline(s, parent, prop)
	}
	switch schemaType(s) {
	case "string":
		if str(get(s, "format")) == "binary" {
			return typ{kind: kFile}, nil
		}
		return typ{kind: kString}, nil
	case "integer":
		if str(get(s, "format")) == "int64" {
			return typ{kind: kInt64}, nil
		}
		return typ{kind: kInt}, nil
	case "number":
		return typ{kind: kFloat}, nil
	case "boolean":
		return typ{kind: kBool}, nil
	case "array":
		elem, err := g.typeOf(get(s, "items"), parent, prop+"Item", where+"[]")
		if err != nil {
			return typ{}, err
		}
		return typ{kind: kArray, elem: &elem}, nil
	case "object":
		if get(s, "properties") != nil {
			return g.inline(s, parent, prop)
		}
		if ap := get(s, "additionalProperties"); ap != nil && schemaType(ap) == "string" {
			return typ{kind: kStringMap}, nil
		}
		return typ{kind: kAnyMap}, nil
	}
	return typ{}, fmt.Errorf("%s: unsupported schema type", where)
}

func (g *generator) inline(s *yaml.Node, parent *model, prop string) (typ, error) {
	inline := &model{
		specName: parent.specName + "." + prop,
		goName:   parent.goName + goName(prop),
		schema:   s,
		request:  parent.request,
		response: parent.response,
	}
	if err := g.fillFields(inline, s); err != nil {
		return typ{}, err
	}
	if err := g.register(inline); err != nil {
		return typ{}, err
	}
	return typ{kind: kObject, name: inline.goName}, nil
}

// goType is the field type: optional numbers and booleans are pointers so that 0 and false can be sent.
func (f field) goType() string {
	return typeString(f.t, !f.required)
}

func typeString(t typ, optional bool) string {
	ptr := ""
	if optional {
		ptr = "*"
	}
	switch t.kind {
	case kString:
		return "string"
	case kInt:
		return ptr + "int"
	case kInt64:
		return ptr + "int64"
	case kFloat:
		return ptr + "float64"
	case kBool:
		return ptr + "bool"
	case kObject:
		return "*" + t.name
	case kFlow:
		return t.name
	case kFile:
		return "*UploadFile"
	case kArray:
		elem := *t.elem
		switch elem.kind {
		case kObject, kFlow:
			return "[]" + elem.name
		case kFile:
			return "[]UploadFile"
		}
		return "[]" + typeString(elem, false)
	case kStringMap:
		return "map[string]string"
	case kAnyMap:
		return "map[string]any"
	}
	panic("unknown kind")
}

func (f field) isScalarPtr() bool {
	return !f.required && (f.t.kind == kInt || f.t.kind == kInt64 || f.t.kind == kFloat || f.t.kind == kBool)
}

// ---- rendering ----

type writer struct{ b strings.Builder }

func (w *writer) p(format string, args ...any) {
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteByte('\n')
}

func comment(w *writer, indent string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			w.p("%s//", indent)
			continue
		}
		w.p("%s// %s", indent, line)
	}
}

func constraintDoc(f field) string {
	s := f.schema
	var parts []string
	if f.required {
		parts = append(parts, "필수")
	}
	if v := str(get(s, "minLength")); v != "" {
		parts = append(parts, "최소 "+v+"자")
	}
	if v := str(get(s, "maxLength")); v != "" {
		parts = append(parts, "최대 "+v+"자")
	}
	if v := str(get(s, "x-max-bytes")); v != "" {
		cs := str(get(s, "x-charset"))
		if cs == "" {
			cs = "UTF-8"
		}
		parts = append(parts, "최대 "+v+"byte("+cs+" 기준)")
	}
	if v := str(get(s, "minItems")); v != "" {
		parts = append(parts, "최소 "+v+"개")
	}
	if v := str(get(s, "maxItems")); v != "" {
		parts = append(parts, "최대 "+v+"개")
	}
	if v := str(get(s, "minimum")); v != "" {
		parts = append(parts, "최솟값 "+v)
	}
	if v := str(get(s, "maximum")); v != "" {
		parts = append(parts, "최댓값 "+v)
	}
	if v := str(get(s, "pattern")); v != "" {
		parts = append(parts, "형식 "+v)
	}
	if v := str(get(s, "x-format")); v != "" {
		parts = append(parts, "형식 "+v)
	}
	if v := strs(get(s, "enum")); len(v) > 0 {
		parts = append(parts, "허용값 "+strings.Join(v, ", "))
	}
	if v := strs(get(s, "x-known-values")); len(v) > 0 {
		parts = append(parts, "알려진 값 "+strings.Join(v, ", "))
	}
	if v := str(get(s, "default")); v != "" {
		parts = append(parts, "서버 기본값 "+v+"(설정하지 않으면 보내지 않음)")
	}
	if f.jsonPart {
		parts = append(parts, "JSON 파트로 전송")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + "."
}

func (g *generator) render() ([]byte, error) {
	w := &writer{}
	w.p("// Code generated by tools/gen from spec/openapi.yaml. DO NOT EDIT.")
	w.p("")
	w.p("package bizgo")
	w.p("")
	w.p("import (")
	w.p("\t\"encoding/json\"")
	w.p("\t\"log/slog\"")
	w.p("\t\"regexp\"")
	w.p(")")
	w.p("")

	body := &writer{}
	for _, m := range g.models {
		if err := g.renderModel(body, m); err != nil {
			return nil, err
		}
	}

	w.p("// Patterns from the spec (request fields).")
	w.p("var (")
	for i, p := range g.patterns {
		w.p("\tpattern%d = regexp.MustCompile(%s)", i, strconv.Quote(p))
	}
	w.p(")")
	w.p("")
	w.b.WriteString(body.b.String())

	// schema name -> Go type, for the spec consistency test
	w.p("// specSchemas maps spec schema names to their Go types (used by tests).")
	w.p("var specSchemas = map[string]any{")
	var names []string
	for _, m := range g.models {
		if !strings.Contains(m.specName, ".") {
			names = append(names, m.specName)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		w.p("\t%q: (*%s)(nil),", n, g.bySpec[n].goName)
	}
	w.p("}")
	return []byte(w.b.String()), nil
}

func (g *generator) patternVar(p string) string {
	idx := slices.Index(g.patterns, p)
	if idx < 0 {
		g.patterns = append(g.patterns, p)
		idx = len(g.patterns) - 1
	}
	return fmt.Sprintf("pattern%d", idx)
}

func modelDoc(m *model) string {
	var b strings.Builder
	if strings.Contains(m.specName, ".") {
		fmt.Fprintf(&b, "%s is the inline object %s of the spec.", m.goName, "`"+m.specName+"`")
	} else {
		fmt.Fprintf(&b, "%s is the spec schema %s.", m.goName, "`"+m.specName+"`")
	}
	if d := strings.TrimSpace(str(get(m.schema, "description"))); d != "" {
		b.WriteString("\n\n" + d)
	}
	if u := strings.TrimSpace(str(get(m.schema, "x-unverified"))); u != "" {
		b.WriteString("\n\n확인되지 않음: " + u)
	}
	if d := requiredIfDoc(m.schema); d != "" {
		b.WriteString("\n\n" + d)
	}
	return b.String()
}

// idempotencyFields returns the idempotencyKey (string) and idempotencyTtl (optional integer) fields
// of m, or nils if m does not have both. Request bodies with both get withDefaultIdempotencyTTL, which
// the service methods call before sending (SDK-DESIGN: a key without a TTL is rejected with A309).
func idempotencyFields(m *model) (key, ttl *field) {
	for i := range m.fields {
		f := &m.fields[i]
		switch {
		case f.json == "idempotencyKey" && f.t.kind == kString:
			key = f
		case f.json == "idempotencyTtl" && f.t.kind == kInt && f.isScalarPtr():
			ttl = f
		}
	}
	if key == nil || ttl == nil {
		return nil, nil
	}
	return key, ttl
}

func (g *generator) renderModel(w *writer, m *model) error {
	if m.flow != nil {
		return g.renderFlow(w, m)
	}
	comment(w, "", modelDoc(m))
	if m.multipart {
		comment(w, "", "It is sent as multipart/form-data. Files are read once, so retries resend the same bytes.")
	}
	if m.response {
		comment(w, "", "Fields that are not in the spec are kept in Extra.")
	}
	comment(w, "", "String masks phone numbers (010****0000) and shows only the length of message content.")
	w.p("type %s struct {", m.goName)
	for _, f := range m.fields {
		doc := strings.TrimSpace(str(get(f.schema, "description")))
		if c := constraintDoc(f); c != "" {
			if doc != "" {
				doc += "\n"
			}
			doc += c
		}
		if u := strings.TrimSpace(str(get(f.schema, "x-unverified"))); u != "" {
			doc += "\n확인되지 않음: " + u
		}
		comment(w, "\t", doc)
		tag := f.json
		if !f.required {
			tag += ",omitzero"
		}
		if f.t.kind == kFile || (f.t.kind == kArray && f.t.elem.kind == kFile) {
			tag = "-"
		}
		w.p("\t%s %s `json:%q`", f.goName, f.goType(), tag)
	}
	if m.response {
		w.p("")
		w.p("\t// Extra holds response fields that are not in the spec, as raw JSON.")
		w.p("\tExtra map[string]json.RawMessage `json:\"-\"`")
	}
	w.p("}")
	w.p("")

	for _, f := range m.fields {
		if !f.isScalarPtr() {
			continue
		}
		base := strings.TrimPrefix(f.goType(), "*")
		w.p("// Get%s returns %s, or the zero value if it is not set.", f.goName, f.goName)
		w.p("func (m *%s) Get%s() %s {", m.goName, f.goName, base)
		w.p("\tif m == nil || m.%s == nil {", f.goName)
		w.p("\t\tvar zero %s", base)
		w.p("\t\treturn zero")
		w.p("\t}")
		w.p("\treturn *m.%s", f.goName)
		w.p("}")
		w.p("")
	}

	g.renderString(w, m)
	if key, ttl := idempotencyFields(m); m.request && g.roots[m.specName] && key != nil {
		comment(w, "", fmt.Sprintf("withDefaultIdempotencyTTL returns m, or a shallow copy of m with %s set to\n"+
			"[DefaultIdempotencyTTL] when %s is set and %s is nil (the API rejects a key without a TTL\n"+
			"with A309). An explicit TTL (0 included) is kept, and m is never modified.", ttl.goName, key.goName, ttl.goName))
		w.p("func (m *%s) withDefaultIdempotencyTTL() *%s {", m.goName, m.goName)
		w.p("\tif m == nil {")
		w.p("\t\treturn nil")
		w.p("\t}")
		w.p("\tttl, ok := defaultIdempotencyTTL(m.%s, m.%s)", key.goName, ttl.goName)
		w.p("\tif !ok {")
		w.p("\t\treturn m")
		w.p("\t}")
		w.p("\tc := *m")
		w.p("\tc.%s = ttl", ttl.goName)
		w.p("\treturn &c")
		w.p("}")
		w.p("")
	}
	if m.request {
		if err := g.renderValidate(w, m); err != nil {
			return err
		}
		if g.roots[m.specName] && !g.hand[m.goName]["Validate"] {
			w.p("// Validate checks the request against the spec (required fields, lengths, byte limits, item")
			w.p("// counts, allowed values, patterns) without sending it. It returns a [*ValidationError] listing")
			w.p("// field paths and reasons, never the values. The service methods call it before sending.")
			w.p("func (m *%s) Validate() error {", m.goName)
			w.p("\tv := &validator{}")
			w.p("\tm.validate(v, \"\")")
			w.p("\treturn v.err()")
			w.p("}")
			w.p("")
		}
	}
	if m.multipart {
		if err := g.renderForm(w, m); err != nil {
			return err
		}
	}
	if m.response {
		g.renderUnmarshal(w, m)
	}
	return nil
}

func (g *generator) renderString(w *writer, m *model) {
	w.p("// String describes the value with phone numbers masked and content shown as its length.")
	w.p("func (m %s) String() string {", m.goName)
	w.p("\tp := newPrinter(%q)", m.goName)
	personal := strings.Contains(strings.ToLower(m.specName), "personalinfo")
	for _, f := range m.fields {
		w.p("\t%s", maskCall("p", f.json, "m."+f.goName, f.t, personal))
	}
	if m.response {
		w.p("\tp.keys(\"extra\", len(m.Extra))")
	}
	w.p("\treturn p.String()")
	w.p("}")
	w.p("")
	w.p("// GoString is String, so that %%#v does not print phone numbers either.")
	w.p("func (m %s) GoString() string { return m.String() }", m.goName)
	w.p("")
	w.p("// LogValue is String, so that log/slog (also its JSON handler) logs the masked form.")
	w.p("func (m %s) LogValue() slog.Value { return slog.StringValue(m.String()) }", m.goName)
	w.p("")
}

func (g *generator) renderUnmarshal(w *writer, m *model) {
	var names []string
	for _, f := range m.fields {
		names = append(names, strconv.Quote(f.json))
	}
	lower := strings.ToLower(m.goName[:1]) + m.goName[1:]
	w.p("var %sFields = []string{%s}", lower, strings.Join(names, ", "))
	w.p("")
	w.p("// UnmarshalJSON decodes the spec fields and keeps the others in Extra.")
	w.p("func (m *%s) UnmarshalJSON(data []byte) error {", m.goName)
	w.p("\ttype plain %s", m.goName)
	w.p("\tvar p plain")
	w.p("\tif err := json.Unmarshal(data, &p); err != nil {")
	w.p("\t\treturn err")
	w.p("\t}")
	w.p("\textra, err := unknownFields(data, %sFields)", lower)
	w.p("\tif err != nil {")
	w.p("\t\treturn err")
	w.p("\t}")
	w.p("\t*m = %s(p)", m.goName)
	w.p("\tm.Extra = extra")
	w.p("\treturn nil")
	w.p("}")
	w.p("")
}

func flowMethod(union string) string {
	if union == primaryFlow {
		return "flowItem"
	}
	return "as" + union
}

func (g *generator) renderFlow(w *writer, m *model) error {
	method := flowMethod(m.goName)
	iface := "ChannelMessage"
	if m.goName != primaryFlow {
		iface = strings.TrimSuffix(m.goName, "MessageFlowItem") + "ChannelMessage"
	}
	comment(w, "", modelDoc(m)+"\n\nExactly one channel field must be set. Channel messages ("+
		memberTypes(m)+") implement "+iface+".")
	w.p("type %s struct {", m.goName)
	for _, fm := range m.flow {
		w.p("\t%s *%s `json:\"%s,omitzero\"`", fm.field, fm.typeName, fm.key)
	}
	w.p("}")
	w.p("")
	if m.goName != primaryFlow {
		w.p("// %s is a channel message that can be put in a %s: %s, or a prepared *%s.", iface, m.goName, memberTypes(m), m.goName)
		w.p("type %s interface {", iface)
		w.p("\t%s() %s", method, m.goName)
		w.p("}")
		w.p("")
	}
	var keyList []string
	for _, fm := range m.flow {
		keyList = append(keyList, fm.key)
	}
	w.p("// String describes the channel message without its content.")
	w.p("func (m %s) String() string {", m.goName)
	w.p("\tp := newPrinter(%q)", m.goName)
	for _, fm := range m.flow {
		w.p("\tp.value(%q, m.%s)", fm.key, fm.field)
	}
	w.p("\treturn p.String()")
	w.p("}")
	w.p("")
	w.p("func (m *%s) validate(v *validator, path string) {", m.goName)
	w.p("\tn := 0")
	for _, fm := range m.flow {
		w.p("\tif m.%s != nil {", fm.field)
		w.p("\t\tn++")
		w.p("\t\tm.%s.validate(v, joinPath(path, %q))", fm.field, fm.key)
		w.p("\t}")
	}
	w.p("\tif n != 1 {")
	w.p("\t\tv.add(path, %q)", "채널 키("+strings.Join(keyList, ", ")+") 중 정확히 하나가 있어야 합니다")
	w.p("\t}")
	w.p("}")
	w.p("")
	w.p("func (m *%s) %s() %s {", m.goName, method, m.goName)
	w.p("\tif m == nil {")
	w.p("\t\treturn %s{}", m.goName)
	w.p("\t}")
	w.p("\treturn *m")
	w.p("}")
	w.p("")
	for _, fm := range m.flow {
		w.p("func (m *%s) %s() %s { return %s{%s: m} }", fm.typeName, method, m.goName, m.goName, fm.field)
		w.p("")
	}
	return nil
}

func memberTypes(m *model) string {
	var names []string
	for _, fm := range m.flow {
		names = append(names, "*"+fm.typeName)
	}
	return strings.Join(names, ", ")
}

func (g *generator) renderValidate(w *writer, m *model) error {
	sites := g.requiredIf[m.specName]
	if len(sites) > 0 {
		renderRequiredIfTable(w, m.goName, sites)
	}
	w.p("func (m *%s) validate(v *validator, path string) {", m.goName)
	if len(m.fields) == 0 {
		w.p("\t_, _ = v, path")
	}
	for _, f := range m.fields {
		if err := g.renderFieldCheck(w, f); err != nil {
			return err
		}
	}
	if len(sites) > 0 {
		// after the field checks: the rules see the whole body, so "$." paths reach the root
		w.p("\tv.requiredIf(path, m, requiredIf%s)", m.goName)
	}
	w.p("}")
	w.p("")
	return nil
}

func (g *generator) renderFieldCheck(w *writer, f field) error {
	s := f.schema
	p := fmt.Sprintf("joinPath(path, %q)", f.json)
	x := "m." + f.goName
	switch f.t.kind {
	case kString:
		checks, err := g.stringChecks(s, p, x, f.path)
		if err != nil {
			return err
		}
		switch {
		case f.required:
			w.p("\tif %s == \"\" {", x)
			w.p("\t\tv.required(%s)", p)
			if len(checks) > 0 {
				w.p("\t} else {")
				for _, c := range checks {
					w.p("\t\t%s", c)
				}
			}
			w.p("\t}")
		case len(checks) > 0:
			w.p("\tif %s != \"\" {", x)
			for _, c := range checks {
				w.p("\t\t%s", c)
			}
			w.p("\t}")
		}
	case kInt, kInt64, kFloat:
		var checks []string
		if v := str(get(s, "minimum")); v != "" {
			checks = append(checks, fmt.Sprintf("v.min(%s, float64(%s), %s)", p, deref(f, x), v))
		}
		if v := str(get(s, "maximum")); v != "" {
			checks = append(checks, fmt.Sprintf("v.max(%s, float64(%s), %s)", p, deref(f, x), v))
		}
		if f.required || len(checks) == 0 {
			for _, c := range checks {
				w.p("\t%s", c)
			}
			return nil
		}
		w.p("\tif %s != nil {", x)
		for _, c := range checks {
			w.p("\t\t%s", c)
		}
		w.p("\t}")
	case kBool:
		// no constraints on booleans in the spec
	case kFile:
		w.p("\tif %s != nil {", x)
		w.p("\t\t%s.validate(v, %s)", x, p)
		if f.required {
			w.p("\t} else {")
			w.p("\t\tv.required(%s)", p)
		}
		w.p("\t}")
	case kStringMap, kAnyMap:
		if f.required {
			w.p("\tif %s == nil {", x)
			w.p("\t\tv.required(%s)", p)
			w.p("\t}")
		}
	case kObject:
		w.p("\tif %s != nil {", x)
		w.p("\t\t%s.validate(v, %s)", x, p)
		if f.required {
			w.p("\t} else {")
			w.p("\t\tv.required(%s)", p)
		}
		w.p("\t}")
	case kArray:
		minItems, err := intp(get(s, "minItems"))
		if err != nil {
			return err
		}
		maxItems, err := intp(get(s, "maxItems"))
		if err != nil {
			return err
		}
		if f.required {
			w.p("\tif len(%s) == 0 {", x)
			w.p("\t\tv.required(%s)", p)
			w.p("\t}")
		}
		if minItems != nil || maxItems != nil {
			lo, hi := int64(-1), int64(-1)
			if minItems != nil {
				lo = *minItems
			}
			if maxItems != nil {
				hi = *maxItems
			}
			cond := fmt.Sprintf("%s != nil", x)
			if f.required {
				cond = fmt.Sprintf("len(%s) > 0", x)
			}
			w.p("\tif %s {", cond)
			w.p("\t\tv.items(%s, len(%s), %d, %d)", p, x, lo, hi)
			w.p("\t}")
		}
		elem := *f.t.elem
		switch elem.kind {
		case kObject, kFlow, kFile:
			w.p("\tfor i := range %s {", x)
			w.p("\t\t%s[i].validate(v, indexPath(%s, i))", x, p)
			w.p("\t}")
		case kString:
			checks, err := g.stringChecks(get(s, "items"), "indexPath("+p+", i)", "item", f.path+"[]")
			if err != nil {
				return err
			}
			if len(checks) > 0 {
				w.p("\tfor i, item := range %s {", x)
				for _, c := range checks {
					w.p("\t\t%s", c)
				}
				w.p("\t}")
			}
		case kAnyMap, kStringMap:
			// free-form objects: nothing to check
		case kInt, kInt64, kFloat, kBool:
			// no item constraints on number arrays in the spec
			if get(get(s, "items"), "minimum") != nil || get(get(s, "items"), "maximum") != nil {
				return fmt.Errorf("%s: number array item constraints are not supported", f.path)
			}
		default:
			return fmt.Errorf("%s: unsupported array item type", f.path)
		}
	case kFlow:
		w.p("\t%s.validate(v, %s)", x, p)
	}
	return nil
}

func deref(f field, x string) string {
	if f.required {
		return x
	}
	return "*" + x
}

func (g *generator) stringChecks(s *yaml.Node, p, x, where string) ([]string, error) {
	var checks []string
	if v := str(get(s, "minLength")); v != "" {
		checks = append(checks, fmt.Sprintf("v.minLength(%s, %s, %s)", p, x, v))
	}
	if v := str(get(s, "maxLength")); v != "" {
		checks = append(checks, fmt.Sprintf("v.maxLength(%s, %s, %s)", p, x, v))
	}
	if v := str(get(s, "x-max-bytes")); v != "" {
		switch cs := strings.ToUpper(str(get(s, "x-charset"))); cs {
		case "EUC-KR":
			checks = append(checks, fmt.Sprintf("v.maxBytesEUCKR(%s, %s, %s)", p, x, v))
		case "", "UTF-8":
			checks = append(checks, fmt.Sprintf("v.maxBytesUTF8(%s, %s, %s)", p, x, v))
		default:
			return nil, fmt.Errorf("%s: unsupported x-charset %q", where, cs)
		}
	}
	if v := str(get(s, "pattern")); v != "" {
		checks = append(checks, fmt.Sprintf("v.pattern(%s, %s, %s)", p, x, g.patternVar(v)))
	}
	if v := strs(get(s, "enum")); len(v) > 0 {
		var quoted []string
		for _, e := range v {
			quoted = append(quoted, strconv.Quote(e))
		}
		checks = append(checks, fmt.Sprintf("v.enum(%s, %s, %s)", p, x, strings.Join(quoted, ", ")))
	}
	return checks, nil
}

// renderForm writes the multipart body of a multipart model: text fields, JSON parts and files, in spec order.
func (g *generator) renderForm(w *writer, m *model) error {
	w.p("func (m *%s) writeForm(f *formWriter) {", m.goName)
	for _, fl := range m.fields {
		x := "m." + fl.goName
		switch {
		case fl.jsonPart:
			w.p("\tf.json(%q, %s)", fl.json, x)
		case fl.t.kind == kFile:
			w.p("\tf.file(%q, %s)", fl.json, x)
		case fl.t.kind == kArray && fl.t.elem.kind == kFile:
			w.p("\tfor i := range %s {", x)
			w.p("\t\tf.file(%q, &%s[i])", fl.json, x)
			w.p("\t}")
		case fl.t.kind == kString:
			w.p("\tf.text(%q, %s)", fl.json, x)
		case fl.t.kind == kInt || fl.t.kind == kInt64 || fl.t.kind == kFloat || fl.t.kind == kBool:
			if fl.required {
				w.p("\tf.value(%q, %s)", fl.json, x)
			} else {
				w.p("\tif %s != nil {", x)
				w.p("\t\tf.value(%q, *%s)", fl.json, x)
				w.p("\t}")
			}
		case fl.t.kind == kObject:
			return fmt.Errorf("%s: object field in a multipart body needs encoding contentType application/json", fl.path)
		default:
			return fmt.Errorf("%s: unsupported multipart field type", fl.path)
		}
	}
	w.p("}")
	w.p("")
	return nil
}

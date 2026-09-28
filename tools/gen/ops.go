package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// This file generates the service methods of every spec operation (services_gen.go), the webhook
// parsers (webhooks_gen.go) and the operation table, from the x-sdk-* metadata of the spec
// (bizgo-api-spec AGENTS.md rule 11, SDK-DESIGN.md §10).

var httpMethods = []string{"get", "put", "post", "delete", "patch"}

// forEachOperation calls fn for every operation in path order, then method order.
func forEachOperation(spec *yaml.Node, fn func(path, method string, op *yaml.Node)) {
	paths := get(spec, "paths")
	for _, p := range keys(paths) {
		item := get(paths, p)
		for _, m := range httpMethods {
			if op := get(item, m); op != nil {
				fn(p, m, op)
			}
		}
	}
}

// Headers a spec parameter may never set: the SDK owns them.
var reservedHeaders = map[string]bool{
	"authorization": true, "user-agent": true, "x-bizgo-client": true, "accept": true, "content-type": true,
	"host": true, "content-length": true, "cookie": true, "proxy-authorization": true,
}

// Go layouts for the documented x-format of date parameters.
var timeLayouts = map[string]string{
	"yyyyMMdd":                 "20060102",
	"YYYYMMDD":                 "20060102",
	"yyyy-MM-dd'T'HH:mm:ss":    "2006-01-02T15:04:05",
	"yyyy-MM-dd'T'HH:mm:ssXXX": "2006-01-02T15:04:05-07:00",
	"yyyy-MM-dd HH:mm:ss":      "2006-01-02 15:04:05",
	"yyyyMMddHHmmss":           "20060102150405",
}

type param struct {
	name     string // API name
	in       string // path, query, header
	required bool
	schema   *yaml.Node
	doc      string
	field    string // Go field name (query/header) or argument name (path)
	goType   string
	layout   string // time layout for x-format parameters
	explode  bool
}

type pagination struct {
	style                           string
	request, size                   string
	items, response, hasNext, total string
}

type operation struct {
	id, method, path     string
	resource, sdkMethod  string
	retry, rate          string
	resultPath           string
	explicitResult       bool
	summary, description string
	op                   *yaml.Node
	pathParams           []param
	queryParams          []param // query and header
	jsonBody, formBody   string  // request schema names
	response             string  // response schema name
	pagination           *pagination
	handwritten          bool // the public method is hand-written (same name on the same type)
	iterHandwritten      bool
	goMethod             string
	serviceType          string
}

type resource struct {
	path     string // dotted
	typeName string
	field    string
	children []*resource
	ops      []*operation
	hand     bool // the type is hand-written
}

type opsGen struct {
	g         *generator
	spec      *yaml.Node
	ops       []*operation
	root      *resource
	byPath    map[string]*resource
	handTypes map[string]map[string]bool // type -> methods
	handFuncs map[string]bool
	usesTime  bool
	usesIter  bool
}

// scanHandwritten finds the types, methods and functions of the hand-written package files.
func scanHandwritten(root string) (map[string]map[string]bool, map[string]bool, error) {
	types := map[string]map[string]bool{}
	funcs := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	for _, file := range files {
		base := filepath.Base(file)
		if strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file) //nolint:gosec // G304: files of the repository
		if err != nil {
			return nil, nil, err
		}
		f, err := parser.ParseFile(fset, base, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						if types[ts.Name.Name] == nil {
							types[ts.Name.Name] = map[string]bool{}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcs[d.Name.Name] = true
					continue
				}
				t := d.Recv.List[0].Type
				if star, ok := t.(*ast.StarExpr); ok {
					t = star.X
				}
				if id, ok := t.(*ast.Ident); ok {
					if types[id.Name] == nil {
						types[id.Name] = map[string]bool{}
					}
					types[id.Name][d.Name.Name] = true
				}
			}
		}
	}
	return types, funcs, nil
}

func serviceTypeName(resourcePath string) string {
	return goName(resourcePath) + "Service"
}

func newOpsGen(g *generator, spec *yaml.Node, root string) (*opsGen, error) {
	hand, funcs, err := scanHandwritten(root)
	if err != nil {
		return nil, err
	}
	o := &opsGen{g: g, spec: spec, handTypes: hand, handFuncs: funcs, byPath: map[string]*resource{}}
	o.root = &resource{}
	var failure error
	names := map[string]string{}
	forEachOperation(spec, func(p, m string, node *yaml.Node) {
		if failure != nil {
			return
		}
		op, err := o.parseOperation(p, m, node)
		if err != nil {
			failure = err
			return
		}
		key := op.resource + "." + op.sdkMethod
		if prev, dup := names[key]; dup {
			failure = fmt.Errorf("%s and %s have the same x-sdk-resource/x-sdk-method %s", prev, op.id, key)
			return
		}
		names[key] = op.id
		o.ops = append(o.ops, op)
		o.resourceFor(op.resource).ops = append(o.resourceFor(op.resource).ops, op)
	})
	if failure != nil {
		return nil, failure
	}
	return o, nil
}

func (o *opsGen) resourceFor(dotted string) *resource {
	if r, ok := o.byPath[dotted]; ok {
		return r
	}
	parent := o.root
	if i := strings.LastIndex(dotted, "."); i >= 0 {
		parent = o.resourceFor(dotted[:i])
	}
	last := dotted[strings.LastIndex(dotted, ".")+1:]
	r := &resource{path: dotted, typeName: serviceTypeName(dotted), field: goName(last)}
	_, r.hand = o.handTypes[r.typeName]
	parent.children = append(parent.children, r)
	o.byPath[dotted] = r
	return r
}

func refName(n *yaml.Node) string { return strings.TrimPrefix(str(get(n, "$ref")), refPrefix) }

func (o *opsGen) resolveParam(n *yaml.Node) *yaml.Node {
	if r := str(get(n, "$ref")); r != "" {
		return path(o.spec, "components", "parameters", strings.TrimPrefix(r, "#/components/parameters/"))
	}
	return n
}

func (o *opsGen) parseOperation(p, method string, node *yaml.Node) (*operation, error) {
	op := &operation{
		id: str(get(node, "operationId")), method: strings.ToUpper(method), path: p, op: node,
		resource: str(get(node, "x-sdk-resource")), sdkMethod: str(get(node, "x-sdk-method")),
		retry: str(get(node, "x-sdk-retry")), rate: str(get(node, "x-sdk-rate")),
		summary: strings.TrimSpace(str(get(node, "summary"))), description: strings.TrimSpace(str(get(node, "description"))),
		resultPath: "data.data",
	}
	if op.id == "" || op.resource == "" || op.sdkMethod == "" {
		return nil, fmt.Errorf("%s %s: operationId, x-sdk-resource and x-sdk-method are required", op.method, p)
	}
	switch op.retry {
	case "safe", "rate_limit_only":
	default:
		return nil, fmt.Errorf("%s: x-sdk-retry must be safe or rate_limit_only", op.id)
	}
	switch op.rate {
	case "":
		op.rate = "other"
	case "send", "other":
	default:
		return nil, fmt.Errorf("%s: x-sdk-rate must be send or other", op.id)
	}
	if r := str(get(node, "x-sdk-result")); r != "" {
		op.resultPath, op.explicitResult = r, true
	}
	op.serviceType = serviceTypeName(op.resource)
	op.goMethod = goName(op.sdkMethod)

	// parameters: path item level first, then operation level (operation wins)
	var raw []*yaml.Node
	raw = append(raw, items(path(o.spec, "paths", p, "parameters"))...)
	raw = append(raw, items(get(node, "parameters"))...)
	seen := map[string]int{}
	var all []param
	for _, r := range raw {
		pn := o.resolveParam(r)
		prm := param{
			name: str(get(pn, "name")), in: str(get(pn, "in")), required: str(get(pn, "required")) == "true",
			schema: get(pn, "schema"), doc: strings.TrimSpace(str(get(pn, "description"))),
		}
		prm.explode = str(get(pn, "explode")) != "false"
		if u := strings.TrimSpace(str(get(pn, "x-unverified"))); u != "" {
			prm.doc += "\n확인되지 않음: " + u
		}
		key := prm.in + ":" + prm.name
		if i, ok := seen[key]; ok {
			all[i] = prm
			continue
		}
		seen[key] = len(all)
		all = append(all, prm)
	}
	for _, prm := range all {
		switch prm.in {
		case "path":
			prm.field = lowerName(prm.name)
			prm.goType = "string"
			if schemaType(prm.schema) != "string" {
				return nil, fmt.Errorf("%s: path parameter %s must be a string", op.id, prm.name)
			}
			op.pathParams = append(op.pathParams, prm)
		case "query", "header":
			if prm.in == "header" && reservedHeaders[strings.ToLower(prm.name)] {
				return nil, fmt.Errorf("%s: header parameter %s is reserved for the SDK", op.id, prm.name)
			}
			if err := o.paramType(op, &prm); err != nil {
				return nil, err
			}
			op.queryParams = append(op.queryParams, prm)
		default:
			return nil, fmt.Errorf("%s: unsupported parameter location %q", op.id, prm.in)
		}
	}
	// path parameters in template order
	var ordered []param
	rest := p
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "}")
		name := rest[i+1 : i+j]
		rest = rest[i+j+1:]
		found := false
		for _, pp := range op.pathParams {
			if pp.name == name {
				ordered = append(ordered, pp)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("%s: path parameter {%s} is not declared", op.id, name)
		}
	}
	if len(ordered) != len(op.pathParams) {
		return nil, fmt.Errorf("%s: declared path parameters do not match the path", op.id)
	}
	op.pathParams = ordered

	content := path(node, "requestBody", "content")
	for _, ct := range keys(content) {
		name := refName(path(content, ct, "schema"))
		switch ct {
		case "application/json":
			op.jsonBody = name
		case "multipart/form-data":
			op.formBody = name
		default:
			return nil, fmt.Errorf("%s: unsupported request content type %s", op.id, ct)
		}
		if name == "" {
			return nil, fmt.Errorf("%s: request body must be a $ref", op.id)
		}
	}
	op.response = refName(path(node, "responses", "200", "content", "application/json", "schema"))
	if op.response == "" {
		op.response = "ApiResponse"
	}
	if pg := get(node, "x-sdk-pagination"); pg != nil {
		op.pagination = &pagination{
			style: str(get(pg, "style")), request: str(get(pg, "request")), size: str(get(pg, "size")),
			items: str(get(pg, "items")), response: str(get(pg, "response")), hasNext: str(get(pg, "hasNext")),
			total: str(get(pg, "total")),
		}
		switch op.pagination.style {
		case "cursor", "page", "offset":
		default:
			return nil, fmt.Errorf("%s: unknown pagination style %q", op.id, op.pagination.style)
		}
	}
	if m, ok := o.handTypes[op.serviceType]; ok {
		op.handwritten = m[op.goMethod]
		op.iterHandwritten = m["Iter"+op.goMethod]
	}
	return op, nil
}

func (o *opsGen) paramType(op *operation, prm *param) error {
	s := prm.schema
	prm.field = goName(prm.name)
	switch schemaType(s) {
	case "string":
		if f := str(get(s, "x-format")); f != "" && get(s, "pattern") == nil {
			layout, ok := timeLayouts[f]
			if !ok {
				return fmt.Errorf("%s: parameter %s: unknown x-format %q (add it to timeLayouts)", op.id, prm.name, f)
			}
			prm.layout = layout
			prm.goType = "time.Time"
			o.usesTime = true
			return nil
		}
		prm.goType = "string"
	case "integer":
		t := "int"
		if str(get(s, "format")) == "int64" {
			t = "int64"
		}
		if prm.required {
			prm.goType = t
		} else {
			prm.goType = "*" + t
		}
	case "boolean":
		prm.goType = "*bool"
		if prm.required {
			prm.goType = "bool"
		}
	case "array":
		if schemaType(get(s, "items")) != "string" {
			return fmt.Errorf("%s: parameter %s: only string arrays are supported", op.id, prm.name)
		}
		prm.goType = "[]string"
	default:
		return fmt.Errorf("%s: parameter %s: unsupported type", op.id, prm.name)
	}
	if prm.in == "header" && prm.goType != "string" {
		return fmt.Errorf("%s: header parameter %s must be a string", op.id, prm.name)
	}
	return nil
}

// ---- response paths ----

// resolve follows a dotted response path (API names; "name[-1]" is the last item) from a schema.
// It returns the Go access expression, the nil guards and the Go type of the value.
func (o *opsGen) resolve(schema, dotted, base string) (expr string, guards []string, goType string, t typ, err error) {
	m := o.g.bySpec[schema]
	if m == nil {
		return "", nil, "", typ{}, fmt.Errorf("unknown schema %s", schema)
	}
	expr = base
	parts := strings.Split(dotted, ".")
	var cur typ
	for i, part := range parts {
		last := false
		if strings.HasSuffix(part, "[-1]") {
			part, last = strings.TrimSuffix(part, "[-1]"), true
		}
		if m == nil {
			return "", nil, "", typ{}, fmt.Errorf("%s: %s is not an object", dotted, strings.Join(parts[:i], "."))
		}
		var f *field
		for k := range m.fields {
			if m.fields[k].json == part {
				f = &m.fields[k]
			}
		}
		if f == nil {
			return "", nil, "", typ{}, fmt.Errorf("%s: %s has no field %s", dotted, m.specName, part)
		}
		expr += "." + f.goName
		cur = f.t
		goType = f.goType()
		if last {
			if cur.kind != kArray {
				return "", nil, "", typ{}, fmt.Errorf("%s: [-1] on a non-array", dotted)
			}
			guards = append(guards, "len("+expr+") == 0")
			expr += "[len(" + expr + ")-1]"
			cur = *cur.elem
			goType = typeString(cur, false)
			if cur.kind == kObject {
				goType = cur.name
			}
		}
		m = nil
		if cur.kind == kObject {
			m = o.g.byGo[cur.name]
			if i < len(parts)-1 && !last {
				guards = append(guards, expr+" == nil")
			}
		}
	}
	return expr, guards, goType, cur, nil
}

// ---- rendering ----

func (o *opsGen) genServices() ([]byte, error) {
	w := &writer{}
	body := &writer{}

	// operation table
	body.p("// Operation metadata of every spec operation (x-sdk-*). Hooks see only these values.")
	body.p("var (")
	for _, op := range o.ops {
		retry := "retrySafe"
		if op.retry == "rate_limit_only" {
			retry = "retryRateLimitOnly"
		}
		pg := ""
		if op.pagination != nil {
			pg = fmt.Sprintf(", pagination: %q", op.pagination.style)
		}
		body.p("\top%s = &operation{id: %q, name: %q, method: %q, path: %q, retry: %s, rate: %q, handwritten: %v%s}",
			goName(op.id), op.id, op.resource+"."+op.sdkMethod, op.method, op.path, retry, op.rate, op.handwritten, pg)
	}
	body.p(")")
	body.p("")
	body.p("// operationTable lists every spec operation in spec order.")
	body.p("var operationTable = []*operation{")
	for _, op := range o.ops {
		body.p("\top%s,", goName(op.id))
	}
	body.p("}")
	body.p("")

	// resource tree
	if err := o.renderServices(body); err != nil {
		return nil, err
	}
	for _, op := range o.ops {
		if err := o.renderOperation(body, op); err != nil {
			return nil, fmt.Errorf("%s: %w", op.id, err)
		}
	}

	w.p("// Code generated by tools/gen from spec/openapi.yaml. DO NOT EDIT.")
	w.p("")
	w.p("package bizgo")
	w.p("")
	w.p("import (")
	w.p("\t\"context\"")
	if o.usesIter {
		w.p("\t\"iter\"")
	}
	w.p("\t\"log/slog\"")
	w.p("\t\"net/url\"")
	if o.usesTime {
		w.p("\t\"time\"")
	}
	w.p(")")
	w.p("")
	w.b.WriteString(body.b.String())
	src := []byte(w.b.String())
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("gofmt services: %w\n%s", err, src)
	}
	return out, nil
}

func (o *opsGen) renderServices(w *writer) error {
	w.p("// Services are the API resources of a [Client], one field per x-sdk-resource of the spec.")
	w.p("// Nested resources are fields of their parent: client.Alimtalk.Templates.List(...).")
	w.p("// The fields are values, so that on a zero Client (not made with NewClient) every method returns a")
	w.p("// ConfigurationError instead of panicking.")
	w.p("type Services struct {")
	for _, r := range o.root.children {
		w.p("\t%s %s // %s", r.field, r.typeName, r.path)
	}
	w.p("}")
	w.p("")
	w.p("func newServices(t *transport) Services {")
	w.p("\treturn Services{")
	for _, r := range o.root.children {
		w.p("\t\t%s: %s,", r.field, o.constructor(r))
	}
	w.p("\t}")
	w.p("}")
	w.p("")
	var walk func(r *resource) error
	walk = func(r *resource) error {
		if r.hand {
			if len(r.children) > 0 {
				return fmt.Errorf("resource %s is hand-written (%s) and cannot get child resources", r.path, r.typeName)
			}
		} else {
			w.p("// %s is the resource client.%s (x-sdk-resource %q).", r.typeName, o.accessPath(r.path), r.path)
			w.p("type %s struct {", r.typeName)
			for _, c := range r.children {
				w.p("\t%s %s // %s", c.field, c.typeName, c.path)
			}
			if len(r.ops) > 0 {
				w.p("")
				w.p("\tt *transport")
			}
			w.p("}")
			w.p("")
			if len(r.ops) > 0 {
				w.p("// tr is nil for a service of a zero Client (not made with NewClient): calls then fail with a ConfigurationError.")
				w.p("func (s *%s) tr() *transport {", r.typeName)
				w.p("\tif s == nil {")
				w.p("\t\treturn nil")
				w.p("\t}")
				w.p("\treturn s.t")
				w.p("}")
				w.p("")
			}
		}
		for _, c := range r.children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range o.root.children {
		if err := walk(r); err != nil {
			return err
		}
	}
	return nil
}

func (o *opsGen) accessPath(dotted string) string {
	var parts []string
	for _, p := range strings.Split(dotted, ".") {
		parts = append(parts, goName(p))
	}
	return strings.Join(parts, ".")
}

func (o *opsGen) constructor(r *resource) string {
	var fields []string
	for _, c := range r.children {
		fields = append(fields, c.field+": "+o.constructor(c))
	}
	if len(r.ops) > 0 {
		fields = append(fields, "t: t")
	}
	return r.typeName + "{" + strings.Join(fields, ", ") + "}"
}

// method variants of one operation: JSON body, multipart body (WithFiles when both exist)
type variant struct {
	name string
	body string // schema
	form bool
}

func (o *opsGen) variants(op *operation) []variant {
	switch {
	case op.jsonBody != "" && op.formBody != "":
		return []variant{{op.goMethod, op.jsonBody, false}, {op.goMethod + "WithFiles", op.formBody, true}}
	case op.formBody != "":
		return []variant{{op.goMethod, op.formBody, true}}
	case op.jsonBody != "":
		return []variant{{op.goMethod, op.jsonBody, false}}
	}
	return []variant{{op.goMethod, "", false}}
}

func paramsType(op *operation) string { return goName(op.id) + "Params" }

func retryDoc(op *operation) string {
	if op.retry == "safe" {
		return "429·5xx·네트워크 오류 재시도"
	}
	return "429만 재시도(중복 실행 방지)"
}

func (o *opsGen) renderOperation(w *writer, op *operation) error {
	if op.handwritten {
		if op.pagination != nil && !op.iterHandwritten {
			return fmt.Errorf("the hand-written %s.%s has no Iter%s", op.serviceType, op.goMethod, op.goMethod)
		}
		return nil
	}
	hasParams := len(op.queryParams) > 0
	if hasParams {
		o.renderParams(w, op)
	}

	// result
	respModel := o.g.bySpec[op.response]
	if respModel == nil {
		return fmt.Errorf("no model for response %s", op.response)
	}
	resultExpr, guards, resultType, _, err := o.resolve(op.response, op.resultPath, "resp")
	void := false
	if err != nil {
		if op.explicitResult {
			return err
		}
		void = true
	}

	for _, v := range o.variants(op) {
		doName := "do" + goName(op.id)
		if v.form && op.jsonBody != "" {
			doName += "WithFiles"
		}
		// signature pieces
		var args, callArgs []string
		for _, pp := range op.pathParams {
			args = append(args, pp.field+" string")
			callArgs = append(callArgs, pp.field)
		}
		bodyType := ""
		if v.body != "" {
			bm := o.g.bySpec[v.body]
			if bm == nil {
				return fmt.Errorf("no model for body %s", v.body)
			}
			bodyType = "*" + bm.goName
			args = append(args, "body "+bodyType)
			callArgs = append(callArgs, "body")
		}
		if hasParams {
			args = append(args, "params "+paramsType(op))
			callArgs = append(callArgs, "params")
		}
		sig := "ctx context.Context"
		if len(args) > 0 {
			sig += ", " + strings.Join(args, ", ")
		}

		// the raw call
		w.p("func %s(ctx context.Context, t *transport%s) (*%s, error) {", doName, prefixComma(args), respModel.goName)
		w.p("\tv := &validator{}")
		if hasParams {
			w.p("\tq, h := url.Values{}, make(map[string]string)")
			w.p("\tparams.encode(v, q, h)")
		}
		if v.body != "" {
			w.p("\tif body == nil {")
			w.p("\t\tv.add(\"\", \"요청 본문이 nil입니다\")")
			w.p("\t} else {")
			w.p("\t\tbody.validate(v, \"\")")
			w.p("\t}")
		}
		w.p("\tif err := v.err(); err != nil {")
		w.p("\t\treturn nil, err")
		w.p("\t}")
		if v.body != "" {
			if key, _ := idempotencyFields(o.g.bySpec[v.body]); key != nil {
				w.p("\tbody = body.withDefaultIdempotencyTTL() // a key without a TTL is rejected (A309); body is not modified")
			}
		}
		if len(op.pathParams) > 0 {
			var names []string
			for _, pp := range op.pathParams {
				names = append(names, pp.field)
			}
			w.p("\tp, err := buildPath(op%s.path, %s)", goName(op.id), strings.Join(names, ", "))
			w.p("\tif err != nil {")
			w.p("\t\treturn nil, err")
			w.p("\t}")
		} else {
			w.p("\tp := op%s.path", goName(op.id))
		}
		w.p("\tr := request{op: op%s, method: %q, path: p, policy: op%s.retry}", goName(op.id), op.method, goName(op.id))
		if hasParams {
			w.p("\tr.query, r.header = q, h")
		}
		if v.body != "" {
			if v.form {
				w.p("\tform, err := buildMultipart(body)")
			} else {
				w.p("\tform, err := buildJSON(body)")
			}
			w.p("\tif err != nil {")
			w.p("\t\treturn nil, err")
			w.p("\t}")
			w.p("\tr.body, r.contentType = form.body, form.contentType")
			if op.rate == "send" {
				bm := o.g.bySpec[v.body]
				for _, f := range bm.fields {
					if f.json == "destinations" && f.t.kind == kArray {
						w.p("\tr.cost = len(body.Destinations)")
					}
				}
			}
		}
		w.p("\treturn call[%s](ctx, t, r)", respModel.goName)
		w.p("}")
		w.p("")

		// public method
		doc := op.summary + "."
		if op.description != "" {
			doc += "\n\n" + op.description
		}
		if v.form && op.jsonBody != "" {
			doc += "\n\n이 메서드는 파일을 첨부해 multipart/form-data로 보냅니다."
		}
		rate := "other(요청 수)"
		if op.rate == "send" {
			rate = "send(수신자 수)"
		}
		doc += fmt.Sprintf("\n\n%s %s (%s). %s, 속도 제한 버킷 %s.", op.method, op.path, op.id, retryDoc(op), rate)
		if u := strings.TrimSpace(str(get(op.op, "x-unverified"))); u != "" {
			doc += "\n\n확인되지 않음: " + u
		}
		if void {
			doc += "\n\n응답에 돌려줄 데이터가 없습니다(성공하면 nil)."
		} else {
			doc += fmt.Sprintf("\n\n응답의 %s 부분을 돌려줍니다. 응답에 없으면 nil이 아닌 빈 값(목록은 빈 슬라이스)입니다.", op.resultPath)
		}
		comment(w, "", v.name+" "+doc)
		if void {
			w.p("func (s *%s) %s(%s) error {", op.serviceType, v.name, sig)
			w.p("\t_, err := %s(ctx, s.tr()%s)", doName, prefixComma(callArgs))
			w.p("\treturn err")
			w.p("}")
			w.p("")
		} else {
			w.p("func (s *%s) %s(%s) (%s, error) {", op.serviceType, v.name, sig, resultType)
			w.p("\tresp, err := %s(ctx, s.tr()%s)", doName, prefixComma(callArgs))
			w.p("\tif err != nil {")
			w.p("\t\treturn %s, err", zeroOf(resultType))
			w.p("\t}")
			// SDK-DESIGN.md §12.19: a success never returns a nil result
			empty := emptyOf(resultType)
			if len(guards) > 0 {
				w.p("\tif %s {", guardList(guards))
				w.p("\t\treturn %s, nil", empty)
				w.p("\t}")
			}
			if zeroOf(resultType) == "nil" {
				w.p("\tif v := %s; v != nil {", resultExpr)
				w.p("\t\treturn v, nil")
				w.p("\t}")
				w.p("\treturn %s, nil", empty)
			} else {
				w.p("\treturn %s, nil", resultExpr)
			}
			w.p("}")
			w.p("")
		}

		if op.pagination != nil && v.name == op.goMethod && !op.iterHandwritten {
			if err := o.renderIterator(w, op, doName, sig, callArgs, respModel); err != nil {
				return err
			}
		}
	}
	return nil
}

func prefixComma(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return ", " + strings.Join(args, ", ")
}

// guardList joins nil guards (resp itself is never nil after a successful call).
func guardList(guards []string) string {
	return strings.Join(guards, " || ")
}

// emptyOf is a non-nil empty value of a result type.
func emptyOf(goType string) string {
	switch {
	case strings.HasPrefix(goType, "*"):
		return "new(" + goType[1:] + ")"
	case strings.HasPrefix(goType, "[]"), strings.HasPrefix(goType, "map["):
		return goType + "{}"
	}
	return zeroOf(goType)
}

func zeroOf(goType string) string {
	switch {
	case strings.HasPrefix(goType, "*"), strings.HasPrefix(goType, "[]"), strings.HasPrefix(goType, "map["):
		return "nil"
	case goType == "string":
		return `""`
	case goType == "bool":
		return "false"
	case goType == "int", goType == "int64", goType == "float64":
		return "0"
	}
	return goType + "{}"
}

func (o *opsGen) findParam(op *operation, name string) *param {
	for i := range op.queryParams {
		if op.queryParams[i].name == name && op.queryParams[i].in == "query" {
			return &op.queryParams[i]
		}
	}
	return nil
}

func (o *opsGen) renderIterator(w *writer, op *operation, doName, sig string, callArgs []string, resp *model) error {
	pg := op.pagination
	o.usesIter = true
	itemsExpr, itemGuards, itemsType, itemsT, err := o.resolve(op.response, pg.items, "resp")
	if err != nil {
		return err
	}
	if itemsT.kind != kArray {
		return fmt.Errorf("pagination items %s is not an array", pg.items)
	}
	elem := strings.TrimPrefix(itemsType, "[]")
	req := o.findParam(op, pg.request)
	if req == nil {
		return fmt.Errorf("pagination request parameter %s is not a query parameter", pg.request)
	}
	comment(w, "", fmt.Sprintf("Iter%s iterates over every item of every page of [%s.%s].", op.goMethod, op.serviceType, op.goMethod))
	switch pg.style {
	case "cursor":
		comment(w, "", fmt.Sprintf("It follows %s (the next cursor is %s) and stops when hasNext is false, the cursor is missing or it does not move.", pg.request, pg.response))
	case "page":
		comment(w, "", fmt.Sprintf("It starts at params.%s (default 1) and stops on an empty page, a page shorter than params.%s, hasNext false or when the total is reached.", req.field, goName(pg.size)))
	case "offset":
		comment(w, "", fmt.Sprintf("It starts at params.%s (default 0) and stops on an empty page, a page shorter than params.%s or when the total is reached.", req.field, goName(pg.size)))
	}
	comment(w, "", "A non-nil error is yielded once, as the last element.")
	w.p("func (s *%s) Iter%s(%s) iter.Seq2[%s, error] {", op.serviceType, op.goMethod, sig, elem)
	w.p("\treturn func(yield func(%s, error) bool) {", elem)
	w.p("\t\tvar zero %s", elem)
	if pg.style != "cursor" {
		if pg.total != "" {
			w.p("\t\tseen := 0")
		}
		start := "1"
		if pg.style == "offset" {
			start = "0"
		}
		if !strings.HasPrefix(req.goType, "*") {
			return fmt.Errorf("pagination parameter %s must be optional", pg.request)
		}
		// a copy: the caller's value is never changed
		w.p("\t\tposition := %s", start)
		w.p("\t\tif params.%s != nil {", req.field)
		w.p("\t\t\tposition = *params.%s", req.field)
		w.p("\t\t}")
		w.p("\t\tparams.%s = &position", req.field)
	}
	w.p("\t\tfor {")
	w.p("\t\t\tresp, err := %s(ctx, s.tr()%s)", doName, prefixComma(callArgs))
	w.p("\t\t\tif err != nil {")
	w.p("\t\t\t\tyield(zero, err)")
	w.p("\t\t\t\treturn")
	w.p("\t\t\t}")
	w.p("\t\t\tvar items %s", itemsType)
	if len(itemGuards) > 0 {
		w.p("\t\t\tif !(%s) {", guardList(itemGuards))
		w.p("\t\t\t\titems = %s", itemsExpr)
		w.p("\t\t\t}")
	} else {
		w.p("\t\t\titems = %s", itemsExpr)
	}
	w.p("\t\t\tfor _, item := range items {")
	w.p("\t\t\t\tif !yield(item, nil) {")
	w.p("\t\t\t\t\treturn")
	w.p("\t\t\t\t}")
	w.p("\t\t\t}")
	boolAt := func(dotted, fallback string) (string, error) {
		if dotted == "" {
			return fallback, nil
		}
		expr, guards, gt, _, err := o.resolve(op.response, dotted, "resp")
		if err != nil {
			return "", err
		}
		w.p("\t\t\tvar hasNext *bool")
		if len(guards) > 0 {
			w.p("\t\t\tif !(%s) {", guardList(guards))
		} else {
			w.p("\t\t\t{")
		}
		if gt == "*bool" {
			w.p("\t\t\t\thasNext = %s", expr)
		} else {
			w.p("\t\t\t\thasNext = Ptr(%s)", expr)
		}
		w.p("\t\t\t}")
		return "hasNext", nil
	}
	switch pg.style {
	case "cursor":
		hn, err := boolAt(pg.hasNext, "")
		if err != nil {
			return err
		}
		hasNext := "len(items) > 0"
		if hn != "" {
			hasNext = "hasNext != nil && *hasNext"
		}
		expr, guards, gt, _, err := o.resolve(op.response, pg.response, "resp")
		if err != nil {
			return err
		}
		ptr := strings.HasPrefix(req.goType, "*")
		w.p("\t\t\tvar next %s", gt)
		if len(guards) > 0 {
			w.p("\t\t\tif !(%s) {", guardList(guards))
			w.p("\t\t\t\tnext = %s", expr)
			w.p("\t\t\t}")
		} else {
			w.p("\t\t\tnext = %s", expr)
		}
		switch {
		case ptr && gt == req.goType:
			w.p("\t\t\tif !advance(&params.%s, %s, next) {", req.field, hasNext)
		case !ptr && gt == req.goType && gt == "string":
			w.p("\t\t\tif !advanceText(&params.%s, %s, next) {", req.field, hasNext)
		default:
			return fmt.Errorf("cursor types differ: parameter %s is %s, response %s is %s", req.name, req.goType, pg.response, gt)
		}
		w.p("\t\t\t\treturn")
		w.p("\t\t\t}")
	default:
		size := o.findParam(op, pg.size)
		if size == nil {
			return fmt.Errorf("pagination size parameter %s is not a query parameter", pg.size)
		}
		if pg.total != "" {
			w.p("\t\t\tseen += len(items)")
		}
		cond := "len(items) == 0"
		if strings.HasPrefix(size.goType, "*") {
			cond += fmt.Sprintf(" || (params.%s != nil && len(items) < *params.%s)", size.field, size.field)
		} else {
			cond += fmt.Sprintf(" || len(items) < params.%s", size.field)
		}
		w.p("\t\t\tif %s {", cond)
		w.p("\t\t\t\treturn")
		w.p("\t\t\t}")
		if pg.hasNext != "" {
			if _, err := boolAt(pg.hasNext, ""); err != nil {
				return err
			}
			w.p("\t\t\tif hasNext != nil && !*hasNext {")
			w.p("\t\t\t\treturn")
			w.p("\t\t\t}")
		}
		if pg.total != "" {
			expr, guards, gt, _, err := o.resolve(op.response, pg.total, "resp")
			if err != nil {
				return err
			}
			cond := ""
			switch gt {
			case "*int", "*int64":
				cond = fmt.Sprintf("%s != nil && int64(seen) >= int64(*%s)", expr, expr)
			case "int", "int64":
				cond = fmt.Sprintf("int64(seen) >= int64(%s)", expr)
			case "string":
				cond = fmt.Sprintf("reachedTextTotal(seen, %s)", expr)
			default:
				return fmt.Errorf("pagination total %s is %s", pg.total, gt)
			}
			if len(guards) > 0 {
				cond = "!(" + guardList(guards) + ") && " + cond
			}
			w.p("\t\t\tif %s {", cond)
			w.p("\t\t\t\treturn")
			w.p("\t\t\t}")
		}
		if pg.style == "page" {
			w.p("\t\t\t*params.%s++", req.field)
		} else {
			w.p("\t\t\t*params.%s += len(items)", req.field)
		}
	}
	w.p("\t\t}")
	w.p("\t}")
	w.p("}")
	w.p("")
	return nil
}

func (o *opsGen) renderParams(w *writer, op *operation) {
	method := op.goMethod
	comment(w, "", fmt.Sprintf("%s are the query and header parameters of [%s.%s] (%s).", paramsType(op), op.serviceType, method, op.id))
	w.p("type %s struct {", paramsType(op))
	for _, prm := range op.queryParams {
		doc := prm.doc
		f := field{json: prm.name, required: prm.required, schema: prm.schema}
		if c := constraintDoc(f); c != "" {
			doc += "\n" + c
		}
		if prm.in == "header" {
			doc += "\nHTTP 헤더 " + prm.name + "로 보냅니다."
		}
		if prm.layout != "" {
			doc += "\n시각은 KST로 바꿔 보냅니다. 0 값이면 보내지 않습니다."
		}
		if prm.goType == "[]string" {
			if prm.explode {
				doc += "\n값마다 파라미터를 반복해 보냅니다."
			} else {
				doc += "\n쉼표로 이어 보냅니다."
			}
		}
		comment(w, "\t", strings.TrimSpace(doc))
		w.p("\t%s %s", prm.field, prm.goType)
	}
	w.p("}")
	w.p("")
	w.p("// String describes the parameters with phone numbers masked and secrets or content shown as their length.")
	w.p("func (p %s) String() string {", paramsType(op))
	w.p("\tpr := newPrinter(%q)", paramsType(op))
	for _, prm := range op.queryParams {
		t := typ{kind: kInt} // printed as a value
		switch prm.goType {
		case "string":
			t = typ{kind: kString}
		case "[]string":
			t = typ{kind: kArray, elem: &typ{kind: kString}}
		}
		w.p("\t%s", maskCall("pr", prm.name, "p."+prm.field, t, false))
	}
	w.p("\treturn pr.String()")
	w.p("}")
	w.p("")
	w.p("// GoString is String, so that %%#v does not print phone numbers either.")
	w.p("func (p %s) GoString() string { return p.String() }", paramsType(op))
	w.p("")
	w.p("// LogValue is String, so that log/slog logs the masked form.")
	w.p("func (p %s) LogValue() slog.Value { return slog.StringValue(p.String()) }", paramsType(op))
	w.p("")
	w.p("func (p *%s) encode(v *validator, q url.Values, h map[string]string) {", paramsType(op))
	usesH := false
	for _, prm := range op.queryParams {
		if prm.in == "header" {
			usesH = true
		}
	}
	if !usesH {
		w.p("\t_ = h")
	}
	for _, prm := range op.queryParams {
		x := "p." + prm.field
		name := strconv.Quote(prm.name)
		set := func(val string) string {
			if prm.in == "header" {
				return fmt.Sprintf("h[%s] = %s", name, val)
			}
			return fmt.Sprintf("q.Set(%s, %s)", name, val)
		}
		switch prm.goType {
		case "string":
			checks, _ := o.g.stringChecks(prm.schema, name, x, op.id+"."+prm.name)
			if prm.in == "header" {
				checks = append(checks, fmt.Sprintf("v.header(%s, %s)", name, x))
			}
			if prm.required {
				w.p("\tif %s == \"\" {", x)
				w.p("\t\tv.required(%s)", name)
				w.p("\t} else {")
			} else {
				w.p("\tif %s != \"\" {", x)
			}
			for _, c := range checks {
				w.p("\t\t%s", c)
			}
			w.p("\t\t%s", set(x))
			w.p("\t}")
		case "time.Time":
			if prm.required {
				w.p("\tif %s.IsZero() {", x)
				w.p("\t\tv.required(%s)", name)
				w.p("\t} else {")
			} else {
				w.p("\tif !%s.IsZero() {", x)
			}
			w.p("\t\t%s", set(fmt.Sprintf("%s.In(KST).Format(%q)", x, prm.layout)))
			w.p("\t}")
		case "int", "int64", "*int", "*int64":
			val := x
			if strings.HasPrefix(prm.goType, "*") {
				val = "*" + x
				w.p("\tif %s != nil {", x)
			} else {
				w.p("\t{")
			}
			if v := str(get(prm.schema, "minimum")); v != "" {
				w.p("\t\tv.min(%s, float64(%s), %s)", name, val, v)
			}
			if v := str(get(prm.schema, "maximum")); v != "" {
				w.p("\t\tv.max(%s, float64(%s), %s)", name, val, v)
			}
			w.p("\t\t%s", set(fmt.Sprintf("formatInt(%s)", val)))
			w.p("\t}")
		case "*bool", "bool":
			val := x
			if prm.goType == "*bool" {
				val = "*" + x
				w.p("\tif %s != nil {", x)
			} else {
				w.p("\t{")
			}
			w.p("\t\t%s", set(fmt.Sprintf("formatBool(%s)", val)))
			w.p("\t}")
		case "[]string":
			if prm.required {
				w.p("\tif len(%s) == 0 {", x)
				w.p("\t\tv.required(%s)", name)
				w.p("\t}")
			}
			w.p("\tif len(%s) > 0 {", x)
			if v := str(get(prm.schema, "minItems")); v != "" {
				w.p("\t\tv.items(%s, len(%s), %s, -1)", name, x, v)
			}
			w.p("\t\tfor i, item := range %s {", x)
			w.p("\t\t\tif item == \"\" {")
			w.p("\t\t\t\tv.add(indexPath(%s, i), \"빈 값입니다\")", name)
			w.p("\t\t\t}")
			w.p("\t\t}")
			if prm.explode {
				w.p("\t\tfor _, item := range %s {", x)
				w.p("\t\t\tq.Add(%s, item)", name)
				w.p("\t\t}")
			} else {
				w.p("\t\tq.Set(%s, joinComma(%s))", name, x)
			}
			w.p("\t}")
		}
	}
	w.p("}")
	w.p("")
}

// ---- webhooks ----

type webhook struct {
	name, id, summary string
	payload, ack      string // schema names
	signed            bool   // the spec declares the X-IB-Signature header
	ackMsgKey         bool   // the ack is {"msgKey": ...}
	ackExample        map[string]string
	hand              bool
}

func (o *opsGen) webhooks() ([]webhook, error) {
	var out []webhook
	hooks := get(o.spec, "webhooks")
	for _, key := range keys(hooks) {
		op := path(hooks, key, "post")
		wh := webhook{
			name: str(get(op, "x-sdk-webhook")), id: str(get(op, "operationId")), summary: str(get(op, "summary")),
			payload: refName(path(op, "requestBody", "content", "application/json", "schema")),
			ack:     refName(path(op, "responses", "200", "content", "application/json", "schema")),
		}
		if wh.name == "" || wh.payload == "" || wh.ack == "" {
			return nil, fmt.Errorf("webhook %s: x-sdk-webhook, payload and ack schemas are required", key)
		}
		wh.signed = o.webhookSigned(op)
		ackModel := o.g.bySpec[wh.ack]
		for _, f := range ackModel.fields {
			if f.json == "msgKey" {
				wh.ackMsgKey = true
			}
		}
		if !wh.ackMsgKey {
			ex := path(op, "responses", "200", "content", "application/json", "example")
			wh.ackExample = map[string]string{}
			for _, k := range keys(ex) {
				wh.ackExample[k] = str(get(ex, k))
			}
			if len(wh.ackExample) == 0 {
				return nil, fmt.Errorf("webhook %s: the ack needs an example", key)
			}
		}
		wh.hand = o.handTypes["WebhookReceiver"][goName(wh.name)]
		out = append(out, wh)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// webhookSigned reports whether a webhook declares the X-IB-Signature header. Those webhooks get a
// verifying receiver method; the others (counsel talk) are not signed by Bizgo and get plain parsers.
func (o *opsGen) webhookSigned(op *yaml.Node) bool {
	for _, p := range items(get(op, "parameters")) {
		pn := o.resolveParam(p)
		if strings.EqualFold(str(get(pn, "name")), "X-IB-Signature") && str(get(pn, "in")) == "header" {
			return true
		}
	}
	return false
}

func (o *opsGen) genWebhooks() ([]byte, error) {
	hooks, err := o.webhooks()
	if err != nil {
		return nil, err
	}
	w := &writer{}
	w.p("// Code generated by tools/gen from spec/openapi.yaml. DO NOT EDIT.")
	w.p("")
	w.p("package bizgo")
	w.p("")
	w.p("import (")
	w.p("\t\"context\"")
	w.p("\t\"net/http\"")
	w.p(")")
	w.p("")
	w.p("// webhookTable lists the webhooks of the spec (x-sdk-webhook) for tests.")
	w.p("var webhookTable = []webhookInfo{")
	for _, wh := range hooks {
		w.p("\t{name: %q, id: %q, signed: %v, handwritten: %v},", wh.name, wh.id, wh.signed, wh.hand)
	}
	w.p("}")
	w.p("")
	for _, wh := range hooks {
		if wh.hand {
			continue
		}
		name := goName(wh.name)
		payload := o.g.bySpec[wh.payload].goName
		ack := o.g.bySpec[wh.ack].goName
		verify := "r.Verify"
		handlerDoc := "verifies the request (401 on failure), parses the body (400 on failure)"
		if wh.signed {
			comment(w, "", fmt.Sprintf("Parse%sWebhook parses a %s webhook body (%s) without verifying the signature.", name, wh.name, wh.summary))
			w.p("func Parse%sWebhook(body []byte) (*%s, error) {", name, payload)
			w.p("\treturn parseWebhook[%s](body)", payload)
			w.p("}")
			w.p("")
			comment(w, "", fmt.Sprintf("%s verifies the headers, then parses a %s webhook body (%s).\n\n"+
				"It always requires a valid signature (X-IB-Timestamp, X-IB-Signature).", name, wh.name, wh.summary))
			w.p("func (r *WebhookReceiver) %s(h http.Header, body []byte) (*%s, error) {", name, payload)
			w.p("\tif err := r.Verify(h); err != nil {")
			w.p("\t\treturn nil, err")
			w.p("\t}")
			w.p("\treturn Parse%sWebhook(body)", name)
			w.p("}")
		} else {
			// Not signed by Bizgo (no X-IB-Signature in the spec): plain parsers, no verification.
			verify = "nil"
			handlerDoc = "parses the body (400 on failure)"
			comment(w, "", fmt.Sprintf("Parse%sWebhook parses a %s webhook body (%s). It needs no receiver and no\n"+
				"webhook secret: this webhook has no signature (Bizgo signs only report and MO webhooks). The body\n"+
				"checks (1MB, JSON depth 64, field types) apply.", name, wh.name, wh.summary))
			w.p("func Parse%sWebhook(body []byte) (*%s, error) {", name, payload)
			w.p("\treturn parseWebhook[%s](body)", payload)
			w.p("}")
			w.p("")
			comment(w, "", fmt.Sprintf("%s parses a %s webhook body (%s), like [Parse%sWebhook].\n\n"+
				"This webhook has no signature, so the headers are not checked: X-IB-Timestamp and\n"+
				"X-IB-Signature are ignored if present.", name, wh.name, wh.summary, name))
			w.p("func (r *WebhookReceiver) %s(_ http.Header, body []byte) (*%s, error) {", name, payload)
			w.p("\treturn Parse%sWebhook(body)", name)
			w.p("}")
		}
		w.p("")
		ackDoc := "It answers {\"msgKey\": ...}."
		ackExpr := fmt.Sprintf("func(p *%s) any { return %s{MsgKey: p.MsgKey} }", payload, ack)
		if !wh.ackMsgKey {
			var fields []string
			var ks []string
			for k := range wh.ackExample {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				fields = append(fields, fmt.Sprintf("%s: %q", goName(k), wh.ackExample[k]))
			}
			ackExpr = fmt.Sprintf("func(*%s) any { return %s{%s} }", payload, ack, strings.Join(fields, ", "))
			ackDoc = "It answers {\"code\": \"A000\", \"result\": \"Success\"}."
		}
		comment(w, "", fmt.Sprintf("%sHandler returns an http.Handler for %s webhooks: it reads at most 1MB, %s,\n"+
			"calls fn (500 without the ack if fn fails, so Bizgo retries) and acknowledges. %s", name, wh.name, handlerDoc, ackDoc))
		w.p("func (r *WebhookReceiver) %sHandler(fn func(context.Context, *%s) error) http.Handler {", name, payload)
		w.p("\treturn webhookHandler(%s, Parse%sWebhook, fn, %s)", verify, name, ackExpr)
		w.p("}")
		w.p("")
	}
	src := []byte(w.b.String())
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("gofmt webhooks: %w\n%s", err, src)
	}
	return out, nil
}

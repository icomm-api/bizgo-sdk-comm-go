package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// x-sdk-required-if (bizgo-api-spec AGENTS.md rule 11, SDK-DESIGN.md §4): conditional required
// fields on request schemas. The generator checks every rule against the schemas and emits, for each
// request body schema (root), a table of the places where rule-bearing objects occur. The generated
// root validate() hands the table to validator.requiredIf (validate.go), which evaluates it on the
// JSON form of the whole body, so that "$." paths see the request root (for example
// $.destinations[].replaceWords of an alimtalk message inside messageFlow).

const requiredIfKey = "x-sdk-required-if"

// requiredIfOps maps the spec operators to the runtime ones (validate.go).
var requiredIfOps = map[string]struct {
	runtime string
	list    bool
}{
	"equals": {"==", false}, "notEquals": {"!=", false}, "in": {"in", true}, "notIn": {"not in", true},
}

type requiredIfRule struct {
	field     string
	op        string // runtime operator
	values    []string
	paths     []string // relative to the object ("required" names and relative requiredPaths)
	rootPaths []string // from the request root, without "$."
}

type requiredIfSite struct {
	at    string // from the request root, "messageFlow[].alimtalk"; "" is the root itself
	rules []requiredIfRule
}

// schemaProps returns the properties of an object schema, following $ref and allOf.
func (g *generator) schemaProps(s *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	var collect func(s *yaml.Node, depth int)
	collect = func(s *yaml.Node, depth int) {
		if s == nil || depth > 32 {
			return
		}
		if r := str(get(s, "$ref")); r != "" {
			collect(get(g.schemas, strings.TrimPrefix(r, refPrefix)), depth+1)
			return
		}
		for _, part := range items(get(s, "allOf")) {
			collect(part, depth+1)
		}
		p := get(s, "properties")
		for _, k := range keys(p) {
			out[k] = get(p, k)
		}
	}
	collect(s, 0)
	return out
}

// deref follows $ref and a single-part allOf.
func (g *generator) derefSchema(s *yaml.Node) *yaml.Node {
	for range 32 {
		if r := str(get(s, "$ref")); r != "" {
			s = get(g.schemas, strings.TrimPrefix(r, refPrefix))
			continue
		}
		if parts := items(get(s, "allOf")); len(parts) == 1 && get(s, "properties") == nil {
			s = parts[0]
			continue
		}
		return s
	}
	return s
}

// checkRequiredPath checks that a dotted path ("a.b", "arr[].x") exists in the schema s.
// It returns false (and no error) when the first property is missing and skipMissingFirst is set.
func (g *generator) checkRequiredPath(s *yaml.Node, p, where string, skipMissingFirst bool) (bool, error) {
	segs := strings.Split(p, ".")
	for i, seg := range segs {
		name, isArray := strings.CutSuffix(seg, "[]")
		if name == "" || strings.ContainsAny(name, "[]$") {
			return false, fmt.Errorf("%s: bad path %q", where, p)
		}
		ps, ok := g.schemaProps(s)[name]
		if !ok {
			if i == 0 && skipMissingFirst {
				return false, nil
			}
			return false, fmt.Errorf("%s: path %q: no property %q", where, p, name)
		}
		ps = g.derefSchema(ps)
		if isArray {
			if schemaType(ps) != "array" {
				return false, fmt.Errorf("%s: path %q: %q is not an array", where, p, name)
			}
			ps = g.derefSchema(get(ps, "items"))
		}
		s = ps
	}
	return true, nil
}

// parseRequiredIf reads and checks the rules of the schema s (an object at a site of root).
func (g *generator) parseRequiredIf(s, root *yaml.Node, where string) ([]requiredIfRule, error) {
	list := get(s, requiredIfKey)
	if list.Kind != yaml.SequenceNode || len(list.Content) == 0 {
		return nil, fmt.Errorf("%s: %s must be a non-empty list", where, requiredIfKey)
	}
	props := g.schemaProps(s)
	var rules []requiredIfRule
	for i, rn := range items(list) {
		w := fmt.Sprintf("%s.%s[%d]", where, requiredIfKey, i)
		for _, k := range keys(rn) {
			if k != "when" && k != "required" && k != "requiredPaths" {
				return nil, fmt.Errorf("%s: unknown key %q", w, k)
			}
		}
		when := get(rn, "when")
		var r requiredIfRule
		r.field = str(get(when, "field"))
		if r.field == "" {
			return nil, fmt.Errorf("%s: when.field is required", w)
		}
		if _, ok := props[r.field]; !ok {
			return nil, fmt.Errorf("%s: when.field %q is not a property", w, r.field)
		}
		nOps := 0
		for _, k := range keys(when) {
			if k == "field" {
				continue
			}
			op, ok := requiredIfOps[k]
			if !ok {
				return nil, fmt.Errorf("%s: unknown operator %q", w, k)
			}
			nOps++
			r.op = op.runtime
			v := get(when, k)
			switch {
			case op.list && v.Kind == yaml.SequenceNode && len(v.Content) > 0:
				r.values = strs(v)
			case !op.list && v.Kind == yaml.ScalarNode:
				r.values = []string{v.Value}
			default:
				return nil, fmt.Errorf("%s: bad value of %q", w, k)
			}
		}
		if nOps != 1 {
			return nil, fmt.Errorf("%s: when needs exactly one operator", w)
		}
		req, reqPaths := get(rn, "required"), get(rn, "requiredPaths")
		if len(items(req)) == 0 && len(items(reqPaths)) == 0 {
			return nil, fmt.Errorf("%s: needs required or requiredPaths", w)
		}
		for _, name := range strs(req) {
			if _, ok := props[name]; !ok {
				return nil, fmt.Errorf("%s: required %q is not a property", w, name)
			}
			r.paths = append(r.paths, name)
		}
		for _, p := range strs(reqPaths) {
			if rest, ok := strings.CutPrefix(p, "$."); ok {
				found, err := g.checkRequiredPath(root, rest, w, true)
				if err != nil {
					return nil, err
				}
				if found { // a root without the first property (e.g. adding reservation recipients): not checked
					r.rootPaths = append(r.rootPaths, rest)
				}
				continue
			}
			if _, err := g.checkRequiredPath(s, p, w, false); err != nil {
				return nil, err
			}
			r.paths = append(r.paths, p)
		}
		if len(r.paths) > 0 || len(r.rootPaths) > 0 {
			rules = append(rules, r)
		}
	}
	return rules, nil
}

// requiredIfSites walks the request body schema root and returns every place where an object
// with x-sdk-required-if occurs.
func (g *generator) requiredIfSites(rootName string) ([]requiredIfSite, error) {
	root := get(g.schemas, rootName)
	var sites []requiredIfSite
	var walk func(s *yaml.Node, at []string, where string, stack []string) error
	walkProp := func(name string, ps *yaml.Node, at []string, where string, stack []string) error {
		seg := name
		if d := g.derefSchema(ps); schemaType(d) == "array" {
			seg += "[]"
			ps = get(d, "items")
			if schemaType(g.derefSchema(ps)) == "array" {
				return nil // arrays of arrays hold no objects with rules in the spec
			}
		}
		return walk(ps, append(slices.Clone(at), seg), where+"."+name, stack)
	}
	walk = func(s *yaml.Node, at []string, where string, stack []string) error {
		if s == nil {
			return nil
		}
		if r := str(get(s, "$ref")); r != "" {
			name := strings.TrimPrefix(r, refPrefix)
			if slices.Contains(stack, name) {
				return nil // recursive schema: already walked above
			}
			return walk(get(g.schemas, name), at, name, append(stack, name))
		}
		if get(s, requiredIfKey) != nil {
			rules, err := g.parseRequiredIf(s, root, where)
			if err != nil {
				return err
			}
			if len(rules) > 0 {
				sites = append(sites, requiredIfSite{at: strings.Join(at, "."), rules: rules})
			}
		}
		for _, part := range items(get(s, "allOf")) {
			if err := walk(part, at, where, stack); err != nil {
				return err
			}
		}
		for _, part := range items(get(s, "oneOf")) { // channel unions: {alimtalk: AlimtalkMessage}
			if err := walk(part, at, where, stack); err != nil {
				return err
			}
		}
		p := get(s, "properties")
		for _, k := range keys(p) {
			if err := walkProp(k, get(p, k), at, where, stack); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, nil, rootName, []string{rootName}); err != nil {
		return nil, err
	}
	return sites, nil
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

// renderRequiredIfTable writes the table of a root, named requiredIf<GoName>.
func renderRequiredIfTable(w *writer, goName string, sites []requiredIfSite) {
	w.p("// requiredIf%s lists the x-sdk-required-if rules of %s, by location from the body root.", goName, goName)
	w.p("var requiredIf%s = []requiredIfSite{", goName)
	for _, s := range sites {
		w.p("\t{at: %q, rules: []requiredIfRule{", s.at)
		for _, r := range s.rules {
			var parts []string
			parts = append(parts, fmt.Sprintf("field: %q, op: %q, values: %s", r.field, r.op, quoteList(r.values)))
			if len(r.paths) > 0 {
				parts = append(parts, "paths: "+quoteList(r.paths))
			}
			if len(r.rootPaths) > 0 {
				parts = append(parts, "rootPaths: "+quoteList(r.rootPaths))
			}
			w.p("\t\t{%s},", strings.Join(parts, ", "))
		}
		w.p("\t}},")
	}
	w.p("}")
	w.p("")
}

// requiredIfDoc describes the rules of a schema for its Go doc comment (no validation).
func requiredIfDoc(s *yaml.Node) string {
	var lines []string
	for _, rn := range items(get(s, requiredIfKey)) {
		when := get(rn, "when")
		cond := ""
		for _, k := range keys(when) {
			op, ok := requiredIfOps[k]
			if !ok {
				continue
			}
			v := str(get(when, k))
			if op.list {
				v = "[" + strings.Join(strs(get(when, k)), ", ") + "]"
			}
			cond = str(get(when, "field")) + " " + op.runtime + " " + v
		}
		fields := append(strs(get(rn, "required")), strs(get(rn, "requiredPaths"))...)
		lines = append(lines, "  - "+cond+": "+strings.Join(fields, ", "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "조건부 필수(x-sdk-required-if, 보내기 전에 검사):\n\n" + strings.Join(lines, "\n")
}

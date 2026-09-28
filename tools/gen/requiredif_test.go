package main

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const requiredIfSpec = `
paths:
  /send:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Req'}
components:
  schemas:
    Req:
      type: object
      properties:
        destinations: {type: array, items: {$ref: '#/components/schemas/Dest'}}
        flow: {type: array, items: {$ref: '#/components/schemas/Msg'}}
    Dest:
      type: object
      properties:
        to: {type: string}
        words: {type: object, additionalProperties: {type: string}}
    Msg:
      type: object
      x-sdk-required-if: RULES
      properties:
        kind: {type: string}
        text: {type: string}
        sub: {type: object, properties: {name: {type: string}}}
`

func requiredIfGen(t *testing.T, rules string) *generator {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Replace(requiredIfSpec, "RULES", rules, 1)), &doc); err != nil {
		t.Fatal(err)
	}
	g, err := newGenerator(doc.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRequiredIfSites(t *testing.T) {
	g := requiredIfGen(t, `[{when: {field: kind, notIn: [a, b]}, required: [text], requiredPaths: [sub.name, "$.destinations[].words", $.missing]}]`)
	sites, err := g.requiredIfSites("Req")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].at != "flow[]" {
		t.Fatalf("sites = %+v", sites)
	}
	r := sites[0].rules[0]
	if r.op != "not in" || strings.Join(r.values, ",") != "a,b" || strings.Join(r.paths, ",") != "text,sub.name" ||
		strings.Join(r.rootPaths, ",") != "destinations[].words" { // $.missing: not in this root, skipped
		t.Fatalf("rule = %+v", r)
	}
}

func TestRequiredIfRejectsBadRules(t *testing.T) {
	for rules, want := range map[string]string{
		`[{when: {field: nope, equals: x}, required: [text]}]`:              "not a property",
		`[{when: {field: kind, equals: x, in: [y]}, required: [text]}]`:     "exactly one operator",
		`[{when: {field: kind, like: x}, required: [text]}]`:                "unknown operator",
		`[{when: {field: kind, equals: x}}]`:                                "needs required or requiredPaths",
		`[{when: {field: kind, equals: x}, required: [nope]}]`:              "not a property",
		`[{when: {field: kind, equals: x}, requiredPaths: [sub.nope]}]`:     "no property",
		`[{when: {field: kind, equals: x}, requiredPaths: ["text[].x"]}]`:   "not an array",
		`[{when: {field: kind, in: x}, required: [text]}]`:                  "bad value",
		`[{when: {field: kind, equals: x}, required: [text], other: true}]`: "unknown key",
	} {
		_, err := requiredIfGen(t, rules).requiredIfSites("Req")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", rules, err, want)
		}
	}
}

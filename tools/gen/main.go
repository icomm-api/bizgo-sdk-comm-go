// Command gen generates, from the vendored spec (spec/openapi.yaml, spec/error-codes.json) of
// github.com/icomm-api/bizgo-sdk-comm-go: the models, the service methods of every operation and
// the operation table (x-sdk-* metadata), the webhook parsers, a test that calls every operation,
// the service error-code table, the EUC-KR (CP949) repertoire table and the spec test data.
// -check compares every one of these files.
//
// It lives in its own module so that its dependencies (a YAML parser and golang.org/x/text,
// used only to build the CP949 table) never become dependencies of the SDK.
//
// Usage (from the repository root):
//
//	go -C tools/gen run . -root ../..          # write the generated files
//	go -C tools/gen run . -root ../.. -check   # fail if they are out of date (CI)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", "../..", "repository root")
	check := flag.Bool("check", false, "only check that the generated files are up to date")
	flag.Parse()

	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	spec, err := loadSpec(filepath.Join(root, "spec", "openapi.yaml"))
	if err != nil {
		return err
	}
	g, err := newGenerator(spec)
	if err != nil {
		return err
	}
	g.hand, _, err = scanHandwritten(root)
	if err != nil {
		return err
	}
	if err := g.parseModels(); err != nil {
		return err
	}
	o, err := newOpsGen(g, spec, root)
	if err != nil {
		return err
	}
	services, err := o.genServices()
	if err != nil {
		return err
	}
	models, err := g.renderModels() // last: it declares the patterns the services use
	if err != nil {
		return err
	}
	webhooks, err := o.genWebhooks()
	if err != nil {
		return err
	}
	opsTest, err := o.genOpsTest()
	if err != nil {
		return err
	}
	codes, err := genErrorCodes(filepath.Join(root, "spec", "error-codes.json"))
	if err != nil {
		return err
	}
	euckr, err := genEUCKR()
	if err != nil {
		return err
	}
	specData, err := genSpecData(spec, o)
	if err != nil {
		return err
	}

	outputs := []struct {
		path string
		data []byte
	}{
		{"models_gen.go", models},
		{"services_gen.go", services},
		{"webhooks_gen.go", webhooks},
		{"operations_gen_test.go", opsTest},
		{"errorcodes_gen.go", codes},
		{"euckr_gen.go", euckr},
		{filepath.Join("testdata", "spec.json"), specData},
	}
	var stale []string
	for _, out := range outputs {
		path := filepath.Join(root, out.path)
		current, err := os.ReadFile(path) //nolint:gosec // G304: fixed output paths under -root
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if bytes.Equal(current, out.data) {
			continue
		}
		if check {
			stale = append(stale, out.path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: a source directory of the repository
			return err
		}
		if err := os.WriteFile(path, out.data, 0o644); err != nil { //nolint:gosec // G306: generated source files are world-readable like the rest of the repository
			return err
		}
		fmt.Println("wrote", out.path)
	}
	if len(stale) > 0 {
		return fmt.Errorf("generated files are out of date: %v (run: go generate ./...)", stale)
	}
	return nil
}

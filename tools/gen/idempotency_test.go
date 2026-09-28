package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A generated operation whose request body has idempotencyKey and idempotencyTtl must fill
// DefaultIdempotencyTTL (a key without a TTL is rejected with A309) on a copy of the body. No
// generated operation has both fields yet, so this adds them to ReservationCreateRequest in a copy
// of the repository, generates, checks the output and builds the package.
func TestGeneratedOperationFillsIdempotencyTTL(t *testing.T) {
	const repo = "../.."
	tmp := t.TempDir()
	files, err := filepath.Glob(filepath.Join(repo, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join(repo, "go.mod"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		copyFile(t, f, filepath.Join(tmp, filepath.Base(f)))
	}
	if err := os.MkdirAll(filepath.Join(tmp, "spec"), 0o755); err != nil { //nolint:gosec // G301: test directory
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(repo, "spec", "error-codes.json"), filepath.Join(tmp, "spec", "error-codes.json"))
	spec, err := os.ReadFile(filepath.Join(repo, "spec", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	anchor := "            $ref: '#/components/schemas/ReservationMessageFlowItem'\n"
	if strings.Count(string(spec), anchor) != 1 {
		t.Fatal("ReservationCreateRequest anchor not found")
	}
	patched := strings.Replace(string(spec), anchor, anchor+
		"        idempotencyKey:\n          type: string\n          maxLength: 200\n"+
		"        idempotencyTtl:\n          type: integer\n          minimum: 0\n          maximum: 86400\n", 1)
	if err := os.WriteFile(filepath.Join(tmp, "spec", "openapi.yaml"), []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(tmp, false); err != nil {
		t.Fatal(err)
	}
	services := readFile(t, filepath.Join(tmp, "services_gen.go"))
	i := strings.Index(services, "func doCreateReservation(")
	if i < 0 {
		t.Fatal("doCreateReservation not generated")
	}
	fn := services[i:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	fill := strings.Index(fn, "body = body.withDefaultIdempotencyTTL()")
	if fill < 0 || fill < strings.Index(fn, "body.validate(") || fill > strings.Index(fn, "buildJSON(body)") {
		t.Fatalf("doCreateReservation does not fill the TTL between validation and serialization:\n%s", fn)
	}
	models := readFile(t, filepath.Join(tmp, "models_gen.go"))
	for _, want := range []string{
		"func (m *ReservationCreateRequest) withDefaultIdempotencyTTL() *ReservationCreateRequest {",
		"func (m *SendOmniRequest) withDefaultIdempotencyTTL() *SendOmniRequest {",
		"\tc := *m\n\tc.IdempotencyTTL = ttl\n\treturn &c\n",
	} {
		if !strings.Contains(models, want) {
			t.Fatalf("models_gen.go lacks %q", want)
		}
	}
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH: generated package not built")
	}
	cmd := exec.Command(goCmd, "build", ".") //nolint:gosec // G204: the go tool on PATH, fixed arguments
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data := readFile(t, from)
	if err := os.WriteFile(to, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test files
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

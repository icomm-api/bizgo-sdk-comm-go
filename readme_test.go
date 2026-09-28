package bizgo_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Go code of README.md must compile and pass go vet (SDK-DESIGN.md §12.13). Blocks are
// type-checked in a temporary module that uses this repository through a replace directive (no
// network): a block that starts with "func " is put at the top level, other blocks become the body
// of a function with ctx, client and the other names the README uses. Blocks that start with
// "import" (other modules, such as the OpenTelemetry adapter) are skipped.

const readmePrelude = `package readme

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	bizgo "github.com/icomm-api/bizgo-sdk-comm-go"
	"github.com/icomm-api/bizgo-sdk-comm-go/bizgotest"
)

var (
	ctx               = context.Background()
	client            *bizgo.Client
	params            bizgo.OmniParams
	msgKey, requestID string
	card, wide        io.Reader
	db                interface {
		Upsert(ctx context.Context, msgKey, code string) error
	}
	enqueue func(context.Context, *bizgo.ReportWebhookPayload) error
)

var (
	_ = errors.Is
	_ = json.Marshal
	_ = fmt.Println
	_ = log.Println
	_ = http.Handle
	_ = httptest.NewRecorder
	_ = os.Getenv
	_ = testing.Short
	_ = time.Now
	_ = bizgotest.New
)
`

func goBlocks(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	var cur []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```go":
			in, cur = true, nil
		case in && strings.TrimSpace(line) == "```":
			in = false
			blocks = append(blocks, strings.Join(cur, "\n"))
		case in:
			cur = append(cur, line)
		}
	}
	return blocks
}

func TestReadmeCodeCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go vet")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not found")
	}
	repo, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	src.WriteString(readmePrelude)
	n := 0
	for i, block := range goBlocks(t, "README.md") {
		trimmed := strings.TrimSpace(block)
		switch {
		case strings.HasPrefix(trimmed, "import"), strings.HasPrefix(trimmed, "package"):
			continue
		case strings.HasPrefix(trimmed, "func "):
			fmt.Fprintf(&src, "\n// README block %d\n%s\n", i, block)
		default:
			fmt.Fprintf(&src, "\n// README block %d\nfunc readmeBlock%d() error {\n%s\nreturn nil\n}\n", i, i, block)
		}
		n++
	}
	if n < 10 {
		t.Fatalf("only %d Go blocks", n)
	}
	dir := t.TempDir()
	mod := "module readme\n\ngo 1.26.0\n\nrequire github.com/icomm-api/bizgo-sdk-comm-go v1.2.0\n\n" +
		"replace github.com/icomm-api/bizgo-sdk-comm-go => " + filepath.ToSlash(repo) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.go"), []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "vet", "./...") //nolint:gosec // G204: the go command found in PATH, fixed arguments
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("README code does not compile:\n%s", out)
	}
}

//go:build scriptagent_integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	scriptagent "github.com/keejkrej/script-agent"
)

// binDir holds binaries built once for the whole integration run.
var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "script-agent-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: create temp dir:", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// repoRoot returns the module root (the parent of this integration directory).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

var (
	cliOnce sync.Once
	cliPath string
	cliErr  error
)

// cliBinary builds the real script-agent binary once per test run with the
// documented release flags and returns its path.
func cliBinary(t *testing.T) string {
	t.Helper()
	cliOnce.Do(func() {
		out := filepath.Join(binDir, "script-agent")
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/script-agent")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			cliErr = fmt.Errorf("build script-agent: %v\n%s", err, b)
			return
		}
		cliPath = out
	})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	return cliPath
}

var (
	artifactsOnce sync.Once
	artifactsPath string
	artifactsErr  error
)

// artifactsBinary builds the real script-agent-artifacts binary once.
func artifactsBinary(t *testing.T) string {
	t.Helper()
	artifactsOnce.Do(func() {
		out := filepath.Join(binDir, "script-agent-artifacts")
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/script-agent-artifacts")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			artifactsErr = fmt.Errorf("build script-agent-artifacts: %v\n%s", err, b)
			return
		}
		artifactsPath = out
	})
	if artifactsErr != nil {
		t.Fatal(artifactsErr)
	}
	return artifactsPath
}

// cliRun is one completed CLI invocation.
type cliRun struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// runCLI executes the built binary in dir. A nil env inherits the parent
// environment; a non-nil env is used verbatim.
func runCLI(t *testing.T, dir string, env []string, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run cli: %v", err)
		}
	}
	return cliRun{ExitCode: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// decodeSingleJSON decodes exactly one JSON object from raw and fails if any
// trailing value follows. It is the "stdout is exactly one bounded JSON
// object" assertion.
func decodeSingleJSON(t *testing.T, raw []byte) scriptagent.Result {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("stdout is empty, want one JSON object")
	}
	if len(raw) > 1<<20 {
		t.Fatalf("stdout is %d bytes, want a bounded JSON object", len(raw))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var res scriptagent.Result
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("decode result: %v\nraw: %s", err, raw)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout has trailing data after the JSON object: err=%v extra=%s", err, extra)
	}
	return res
}

// outputText concatenates every text output item in order.
func outputText(res scriptagent.Result) string {
	var b strings.Builder
	for _, o := range res.Output {
		if o.Kind == "text" {
			b.WriteString(o.Text)
		}
	}
	return b.String()
}

// streamText concatenates text output for one stream.
func streamText(res scriptagent.Result, stream string) string {
	var b strings.Builder
	for _, o := range res.Output {
		if o.Kind == "text" && o.Stream == stream {
			b.WriteString(o.Text)
		}
	}
	return b.String()
}

func requireStatus(t *testing.T, res scriptagent.Result, want scriptagent.Status) {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status = %q, want %q (error = %+v, output = %+v)", res.Status, want, res.Error, res.Output)
	}
}

// newAgent creates an Agent with a fresh workspace and registers cleanup.
func newAgent(t *testing.T, cfg scriptagent.Config) *scriptagent.Agent {
	t.Helper()
	if cfg.Workspace == "" {
		cfg.Workspace = t.TempDir()
	}
	a, err := scriptagent.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return a
}

// runAgent is a convenience wrapper that runs one request and fails on a
// transport-level surprise.
func runAgent(t *testing.T, a *scriptagent.Agent, req scriptagent.Request) scriptagent.Result {
	t.Helper()
	return a.Run(context.Background(), req)
}

// copyTree copies src into dst, skipping VCS metadata and root-level build
// outputs. It preserves regular file modes.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".test") {
			return nil
		}
		if !strings.Contains(rel, string(filepath.Separator)) {
			switch d.Name() {
			case "script-agent", "script-agent-artifacts":
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}

// runGo runs the go tool in dir and fails the test on a non-zero exit.
func runGo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

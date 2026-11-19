// Command script-agent-artifacts lists and verifies the runtime artifacts that
// script-agent embeds or vendors.
//
// Usage:
//
//	script-agent-artifacts list
//	script-agent-artifacts verify [--repo <path>]
//
// "list" prints the QuickJS and QuickPy artifact locations, sizes, hashes and
// engine versions. "verify" checks the on-disk copies against the recorded
// metadata and checks the embedded QuickPy bytes. --repo defaults to the
// nearest parent of the working directory whose go.mod declares this module.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keejkrej/script-agent/internal/artifacts"
)

const (
	quickJSPath   = "third_party/pi-go/internal/quickjs/quickjs.wasm"
	quickJSSize   = 637405
	quickJSSHA256 = "d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9"
	quickJSEngine = "QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0)"
	quickPyPath   = "internal/artifacts/quickpy.wasm"
	modulePath    = "github.com/keejkrej/script-agent"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: script-agent-artifacts <list|verify> [flags]")
		return 2
	}
	switch args[0] {
	case "list":
		return runList(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintf(stdout, "quickjs path=%s size=%d sha256=%s engine=%q\n", quickJSPath, quickJSSize, quickJSSHA256, quickJSEngine)
	meta := artifacts.QuickPyMetadata()
	fmt.Fprintf(stdout, "quickpy path=%s size=%d sha256=%s abi=%d engine=%q\n", quickPyPath, meta.Size, meta.SHA256, meta.ABI, meta.Engine)
	return 0
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository root (defaults to the nearest parent of the working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root := *repo
	if root == "" {
		found, err := findRepoRoot()
		if err != nil {
			fmt.Fprintf(stderr, "verify: %v\n", err)
			return 1
		}
		root = found
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return 1
	}
	if err := verifyFile(filepath.Join(abs, quickJSPath), quickJSSize, quickJSSHA256); err != nil {
		fmt.Fprintf(stderr, "verify: quickjs: %v\n", err)
		return 1
	}
	if err := verifyFile(filepath.Join(abs, quickPyPath), artifacts.QuickPySize, artifacts.QuickPySHA256); err != nil {
		fmt.Fprintf(stderr, "verify: quickpy: %v\n", err)
		return 1
	}
	if err := artifacts.VerifyQuickPy(); err != nil {
		fmt.Fprintf(stderr, "verify: embedded quickpy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ok: %s size=%d sha256=%s\n", quickJSPath, quickJSSize, quickJSSHA256)
	fmt.Fprintf(stdout, "ok: %s size=%d sha256=%s\n", quickPyPath, artifacts.QuickPySize, artifacts.QuickPySHA256)
	return 0
}

func verifyFile(path string, wantSize int, wantSHA string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("%s: empty", path)
	}
	if len(data) != wantSize {
		return fmt.Errorf("%s: size = %d, want %d", path, len(data), wantSize)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return fmt.Errorf("%s: sha256 = %s, want %s", path, got, wantSHA)
	}
	return nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isRepoRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find repository root (no go.mod declaring " + modulePath + ")")
		}
		dir = parent
	}
}

func isRepoRoot(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "module "+modulePath {
			return true
		}
	}
	return false
}

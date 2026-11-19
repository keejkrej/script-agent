package process

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateEmptyArgv(t *testing.T) {
	_, err := Run(context.Background(), Request{Dir: t.TempDir()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateEmptyExecutable(t *testing.T) {
	_, err := Run(context.Background(), Request{Argv: []string{""}, Dir: t.TempDir()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateNULArg(t *testing.T) {
	_, err := Run(context.Background(), Request{Argv: []string{"bash", "a\x00b"}, Dir: t.TempDir()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateEnvNUL(t *testing.T) {
	_, err := Run(context.Background(), Request{
		Argv: []string{"true"},
		Dir:  t.TempDir(),
		Env:  []string{"A=b\x00c"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateNegativeCaps(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}, Dir: dir, MaxStdoutBytes: -1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("stdout cap err = %v, want ErrInvalidRequest", err)
	}
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}, Dir: dir, MaxStderrBytes: -1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("stderr cap err = %v, want ErrInvalidRequest", err)
	}
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}, Dir: dir, Timeout: -1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("timeout err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateDir(t *testing.T) {
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty dir err = %v, want ErrInvalidRequest", err)
	}
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}, Dir: "relative"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("relative dir err = %v, want ErrInvalidRequest", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Run(context.Background(), Request{Argv: []string{"true"}, Dir: missing}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing dir err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidateNilContext(t *testing.T) {
	//nolint:staticcheck // deliberately passing a nil context
	_, err := Run(nil, Request{Argv: []string{"true"}, Dir: t.TempDir()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestRunUnsupportedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	_, err := Run(context.Background(), Request{Argv: []string{"cmd"}, Dir: t.TempDir()})
	if !errors.Is(err, errUnsupported) {
		t.Fatalf("err = %v, want errUnsupported", err)
	}
}

func TestCappedWriter(t *testing.T) {
	fired := 0
	w := newCappedWriter(4, func() { fired++ })
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if n, err := w.Write([]byte("def")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	buf, trunc := w.snapshot()
	if string(buf) != "abcd" {
		t.Fatalf("buf = %q, want %q", buf, "abcd")
	}
	if !trunc {
		t.Fatal("truncated = false, want true")
	}
	if fired != 1 {
		t.Fatalf("overflow fired %d times, want 1", fired)
	}
	if _, _ = w.Write([]byte("ghi")); fired != 1 {
		t.Fatalf("overflow fired again: %d", fired)
	}
}

func TestCappedWriterExactCap(t *testing.T) {
	fired := 0
	w := newCappedWriter(4, func() { fired++ })
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	buf, trunc := w.snapshot()
	if string(buf) != "abcd" || trunc || fired != 0 {
		t.Fatalf("buf=%q trunc=%v fired=%d", buf, trunc, fired)
	}
}

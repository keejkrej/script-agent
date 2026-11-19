package scriptagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func TestBashEngineArgvAndEnvironment(t *testing.T) {
	var got execRequest
	b := &bashEngine{
		cfg: Config{Workspace: "/ws", AllowProcess: true, MaxOutputBytes: 1024},
		exec: func(ctx context.Context, req execRequest) (execResult, error) {
			got = req
			return execResult{ExitCode: intPtr(0)}, nil
		},
		findBash: func() (string, error) { return "/bin/bash", nil },
	}
	res := b.Run(context.Background(), engineRequest{
		Source:         "echo hi",
		Timeout:        time.Second,
		MaxOutputBytes: 1024,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s (%+v)", res.Status, res.Error)
	}
	wantArgv := []string{"/bin/bash", "--noprofile", "--norc", "-c", "echo hi", "script-agent"}
	if len(got.Argv) != len(wantArgv) {
		t.Fatalf("argv = %v", got.Argv)
	}
	for i := range wantArgv {
		if got.Argv[i] != wantArgv[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got.Argv[i], wantArgv[i])
		}
	}
	if got.Dir != "/ws" {
		t.Fatalf("dir = %q", got.Dir)
	}
	if got.Timeout != time.Second {
		t.Fatalf("timeout = %s", got.Timeout)
	}
	if got.MaxStdoutBytes != 1024 || got.MaxStderrBytes != 1024 {
		t.Fatalf("output limits = %d/%d", got.MaxStdoutBytes, got.MaxStderrBytes)
	}
	wantEnv := map[string]string{
		"PATH":   "/usr/local/bin:/usr/bin:/bin",
		"HOME":   "/ws",
		"PWD":    "/ws",
		"LANG":   "C.UTF-8",
		"LC_ALL": "C.UTF-8",
	}
	seen := map[string]string{}
	for _, kv := range got.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		seen[k] = v
	}
	for k, v := range wantEnv {
		if seen[k] != v {
			t.Fatalf("env %s = %q, want %q", k, seen[k], v)
		}
	}
	if !strings.HasPrefix(seen["TMPDIR"], "/") {
		t.Fatalf("TMPDIR = %q", seen["TMPDIR"])
	}
	if len(got.Env) != 6 {
		t.Fatalf("env has %d entries, want 6: %v", len(got.Env), got.Env)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "full OS privileges") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestBashEngineMapResult(t *testing.T) {
	cases := []struct {
		name     string
		res      execResult
		err      error
		want     Status
		wantExit *int
	}{
		{"ok", execResult{ExitCode: intPtr(0)}, nil, StatusCompleted, intPtr(0)},
		{"exit", execResult{ExitCode: intPtr(2)}, nil, StatusFailed, intPtr(2)},
		{"timeout", execResult{TimedOut: true}, nil, StatusTimedOut, nil},
		{"output", execResult{OutputExceeded: true}, nil, StatusLimitExceeded, nil},
		{"deadline", execResult{}, context.DeadlineExceeded, StatusTimedOut, nil},
		{"cancel", execResult{}, context.Canceled, StatusCancelled, nil},
		{"error", execResult{}, errors.New("boom"), StatusFailed, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &bashEngine{cfg: Config{Workspace: "/ws", AllowProcess: true}}
			er := b.mapResult(tc.res, tc.err)
			if er.Status != tc.want {
				t.Fatalf("status = %s, want %s", er.Status, tc.want)
			}
			if tc.wantExit == nil {
				if er.ExitCode != nil {
					t.Fatalf("exit code = %v, want nil", *er.ExitCode)
				}
			} else if er.ExitCode == nil || *er.ExitCode != *tc.wantExit {
				t.Fatalf("exit code = %v, want %d", er.ExitCode, *tc.wantExit)
			}
			if len(er.Warnings) == 0 {
				t.Fatal("missing bash privilege warning")
			}
		})
	}
}

func TestBashEngineDeniedBeforeLookup(t *testing.T) {
	looked := false
	called := false
	b := &bashEngine{
		cfg: Config{AllowProcess: false},
		exec: func(ctx context.Context, req execRequest) (execResult, error) {
			called = true
			return execResult{}, nil
		},
		findBash: func() (string, error) {
			looked = true
			return "/bin/bash", nil
		},
	}
	res := b.Run(context.Background(), engineRequest{Source: "x"})
	if res.Status != StatusDenied {
		t.Fatalf("status = %s", res.Status)
	}
	if looked || called {
		t.Fatalf("looked=%v called=%v, want both false", looked, called)
	}
}

func TestBashEngineMissingBash(t *testing.T) {
	b := &bashEngine{
		cfg:      Config{AllowProcess: true},
		findBash: func() (string, error) { return "", errors.New("no bash") },
	}
	res := b.Run(context.Background(), engineRequest{Source: "x"})
	if res.Status != StatusUnsupported {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestFindBash(t *testing.T) {
	path, err := findBash()
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	if path == "" {
		t.Fatal("findBash returned an empty path")
	}
}

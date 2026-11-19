//go:build scriptagent_integration

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	scriptagent "github.com/keejkrej/script-agent"
)

// TestOutputLimitPython proves the Python runtime returns limit_exceeded with
// the retained byte prefix, and that a valid run under the same limit has only
// the normal completed fields.
func TestOutputLimitPython(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxOutputBytes: 10, Timeout: 20 * time.Second})

	over := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   `print("A" * 100)`,
	})
	requireStatus(t, over, scriptagent.StatusLimitExceeded)
	if over.Error == nil || over.Error.Name != "LimitExceeded" {
		t.Fatalf("error = %+v", over.Error)
	}
	if got := outputText(over); got != "AAAAAAAAAA" {
		t.Fatalf("retained prefix = %q, want 10 bytes", got)
	}

	ok := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   `print("ok")`,
	})
	requireStatus(t, ok, scriptagent.StatusCompleted)
	if ok.Error != nil {
		t.Fatalf("valid run has an error: %+v", ok.Error)
	}
	if got := outputText(ok); got != "ok\n" {
		t.Fatalf("valid output = %q", got)
	}
}

// TestOutputLimitJavaScript proves the same for the QuickJS sandbox.
func TestOutputLimitJavaScript(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxOutputBytes: 10, Timeout: 20 * time.Second})

	over := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   `text("A".repeat(100));`,
	})
	requireStatus(t, over, scriptagent.StatusLimitExceeded)
	if over.Error == nil || over.Error.Name != "LimitExceeded" {
		t.Fatalf("error = %+v", over.Error)
	}
	if got := outputText(over); got != "AAAAAAAAAA" {
		t.Fatalf("retained prefix = %q, want 10 bytes", got)
	}

	ok := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   `text("ok");`,
	})
	requireStatus(t, ok, scriptagent.StatusCompleted)
	if ok.Error != nil {
		t.Fatalf("valid run has an error: %+v", ok.Error)
	}
	if got := outputText(ok); got != "ok" {
		t.Fatalf("valid output = %q", got)
	}
}

// TestSourceLimitLib proves the public Agent rejects an oversized source
// before any engine runs.
func TestSourceLimitLib(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxSourceBytes: 5, Timeout: 20 * time.Second})
	res := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   `print("hello")`,
	})
	requireStatus(t, res, scriptagent.StatusInvalidRequest)
	if res.Error == nil || res.Error.Code != "source_too_large" {
		t.Fatalf("error = %+v", res.Error)
	}
}

// TestRequestTimeoutTighten proves a Request may only tighten the Agent
// timeout, and that a tighter timeout produces timed_out.
func TestRequestTimeoutTighten(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), Timeout: 5 * time.Second})

	loose := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   `print(1)`,
		Timeout:  10 * time.Second,
	})
	requireStatus(t, loose, scriptagent.StatusInvalidRequest)
	if loose.Error == nil || loose.Error.Code != "timeout" {
		t.Fatalf("error = %+v", loose.Error)
	}

	tight := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   "x = 0\nwhile True:\n    x += 1\n",
		Timeout:  300 * time.Millisecond,
	})
	requireStatus(t, tight, scriptagent.StatusTimedOut)
}

// TestOuterDeadline proves an earlier caller deadline wins and is reported as
// timed_out, not as a normal completion.
func TestOuterDeadline(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := a.Run(ctx, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   "x = 0\nwhile True:\n    x += 1\n",
	})
	elapsed := time.Since(start)
	requireStatus(t, res, scriptagent.StatusTimedOut)
	if elapsed > 8*time.Second {
		t.Fatalf("deadline took %s, want a bounded return", elapsed)
	}
}

// TestOuterCancellation proves caller cancellation is reported as cancelled.
func TestOuterCancellation(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), Timeout: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	res := a.Run(ctx, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   `while (true) {}`,
	})
	requireStatus(t, res, scriptagent.StatusCancelled)
}

// TestConcurrentRuns proves the public Agent runs independent executions
// concurrently up to MaxExecutions.
func TestConcurrentRuns(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxExecutions: 4, Timeout: 20 * time.Second})
	const n = 4
	results := make([]scriptagent.Result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			lang := scriptagent.LanguagePython
			src := `print("py")`
			if i%2 == 1 {
				lang = scriptagent.LanguageJavaScript
				src = `text("js")`
			}
			results[i] = a.Run(context.Background(), scriptagent.Request{Language: lang, Source: src})
		}(i)
	}
	wg.Wait()
	for i, res := range results {
		requireStatus(t, res, scriptagent.StatusCompleted)
		if got := outputText(res); got != "py\n" && got != "js" {
			t.Fatalf("result %d output = %q", i, got)
		}
	}
}

// TestCloseIdempotentAndNoLeak proves Close is idempotent, waits for an
// in-flight execution, and rejects later runs.
func TestCloseIdempotentAndNoLeak(t *testing.T) {
	a, err := scriptagent.New(scriptagent.Config{
		Workspace:     t.TempDir(),
		Timeout:       10 * time.Second,
		MaxExecutions: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan scriptagent.Result, 1)
	go func() {
		done <- a.Run(ctx, scriptagent.Request{
			Language: scriptagent.LanguagePython,
			Source:   "x = 0\nwhile True:\n    x += 1\n",
		})
	}()
	time.Sleep(200 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		closeDone <- a.Close(closeCtx)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		requireStatus(t, res, scriptagent.StatusCancelled)
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight run did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}

	// Idempotent.
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// No new work after close.
	after := a.Run(context.Background(), scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   `print(1)`,
	})
	requireStatus(t, after, scriptagent.StatusInvalidRequest)
	if after.Error == nil || after.Error.Code != "closed" {
		t.Fatalf("after-close error = %+v", after.Error)
	}
}

// TestOutputLimitErrorIsNotNormal proves a limit result is never reported as
// a normal completion: the status and error are both present.
func TestOutputLimitErrorIsNotNormal(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxOutputBytes: 4, Timeout: 20 * time.Second})
	res := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   `text("abcdefgh");`,
	})
	requireStatus(t, res, scriptagent.StatusLimitExceeded)
	if res.Error == nil {
		t.Fatal("limit result has no error")
	}
	if !strings.Contains(strings.ToLower(res.Error.Name), "limit") {
		t.Fatalf("error name = %q", res.Error.Name)
	}
}

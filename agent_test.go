package scriptagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeEngine struct {
	runFn   func(ctx context.Context, req engineRequest) engineResult
	closeFn func(ctx context.Context) error

	mu     sync.Mutex
	closed bool
	runs   int
}

func (f *fakeEngine) Run(ctx context.Context, req engineRequest) engineResult {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if f.runFn != nil {
		return f.runFn(ctx, req)
	}
	return engineResult{Status: StatusCompleted}
}

func (f *fakeEngine) Close(ctx context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	if f.closeFn != nil {
		return f.closeFn(ctx)
	}
	return nil
}

func (f *fakeEngine) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func newTestAgent(cfg Config, engines map[Language]engine) *Agent {
	if cfg.MaxExecutions == 0 {
		cfg.MaxExecutions = 1
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if cfg.MaxSourceBytes == 0 {
		cfg.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Agent{
		cfg:      cfg,
		engines:  engines,
		sem:      make(chan struct{}, cfg.MaxExecutions),
		closedCh: make(chan struct{}),
	}
}

func TestRunDispatchesByLanguage(t *testing.T) {
	var got []Language
	var mu sync.Mutex
	mk := func(lang Language) engine {
		return &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
			mu.Lock()
			got = append(got, lang)
			mu.Unlock()
			return engineResult{Status: StatusCompleted}
		}}
	}
	a := newTestAgent(Config{AllowProcess: true}, map[Language]engine{
		LanguagePython:     mk(LanguagePython),
		LanguageJavaScript: mk(LanguageJavaScript),
		LanguageBash:       mk(LanguageBash),
	})
	for _, lang := range []Language{LanguagePython, LanguageJavaScript, LanguageBash} {
		res := a.Run(context.Background(), Request{Language: lang, Source: "x", AllowProcess: lang == LanguageBash})
		if res.Status != StatusCompleted {
			t.Fatalf("%s: status = %s", lang, res.Status)
		}
	}
	if len(got) != 3 {
		t.Fatalf("runs = %v", got)
	}
}

func TestRunInvalidRequests(t *testing.T) {
	a := newTestAgent(Config{MaxSourceBytes: 4, MaxOutputBytes: 8, Timeout: time.Second}, map[Language]engine{
		LanguagePython: &fakeEngine{},
		LanguageBash:   &fakeEngine{},
	})
	cases := []struct {
		name string
		req  Request
	}{
		{"language", Request{Language: "ruby", Source: "x"}},
		{"source", Request{Language: LanguagePython, Source: "12345"}},
		{"timeout", Request{Language: LanguagePython, Source: "x", Timeout: 2 * time.Second}},
		{"max_output", Request{Language: LanguagePython, Source: "x", MaxOutputBytes: 9}},
		{"allow_process", Request{Language: LanguagePython, Source: "x", AllowProcess: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := a.Run(context.Background(), tc.req)
			if res.Status != StatusInvalidRequest {
				t.Fatalf("status = %s, want invalid_request", res.Status)
			}
			if res.Error == nil || res.Error.Name != "InvalidRequest" {
				t.Fatalf("error = %+v", res.Error)
			}
		})
	}
}

func TestRunBashDenied(t *testing.T) {
	eng := &fakeEngine{}
	a := newTestAgent(Config{AllowProcess: false}, map[Language]engine{LanguageBash: eng})
	res := a.Run(context.Background(), Request{Language: LanguageBash, Source: "x", AllowProcess: true})
	if res.Status != StatusDenied {
		t.Fatalf("status = %s, want denied", res.Status)
	}
	if eng.runs != 0 {
		t.Fatal("engine ran despite denial")
	}
}

func TestRunBashRequiresRequestGrant(t *testing.T) {
	eng := &fakeEngine{}
	a := newTestAgent(Config{AllowProcess: true}, map[Language]engine{LanguageBash: eng})
	res := a.Run(context.Background(), Request{Language: LanguageBash, Source: "x"})
	if res.Status != StatusDenied {
		t.Fatalf("status = %s, want denied", res.Status)
	}
	if eng.runs != 0 {
		t.Fatal("engine ran without Request.AllowProcess")
	}
}

func TestRunCapacityWaitsAndCancels(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	eng := &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
		close(started)
		<-release
		return engineResult{Status: StatusCompleted}
	}}
	a := newTestAgent(Config{MaxExecutions: 1}, map[Language]engine{LanguagePython: eng})

	done := make(chan Result, 1)
	go func() {
		done <- a.Run(context.Background(), Request{Language: LanguagePython, Source: "x"})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := a.Run(ctx, Request{Language: LanguagePython, Source: "x"})
	if res.Status != StatusCancelled {
		t.Fatalf("second run status = %s, want cancelled", res.Status)
	}
	close(release)
	if first := <-done; first.Status != StatusCompleted {
		t.Fatalf("first run status = %s", first.Status)
	}
}

func TestCloseWaitsForInflightAndClosesEngines(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var closedAfterRun atomic.Bool
	eng := &fakeEngine{
		runFn: func(ctx context.Context, req engineRequest) engineResult {
			close(started)
			<-release
			return engineResult{Status: StatusCompleted}
		},
		closeFn: func(ctx context.Context) error {
			closedAfterRun.Store(true)
			return nil
		},
	}
	a := newTestAgent(Config{MaxExecutions: 1}, map[Language]engine{LanguagePython: eng})

	runDone := make(chan Result, 1)
	go func() {
		runDone <- a.Run(context.Background(), Request{Language: LanguagePython, Source: "x"})
	}()
	<-started

	closeDone := make(chan error, 1)
	go func() { closeDone <- a.Close(context.Background()) }()

	select {
	case <-closeDone:
		t.Fatal("Close returned before the in-flight run finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if res := <-runDone; res.Status != StatusCompleted {
		t.Fatalf("run status = %s", res.Status)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !eng.isClosed() || !closedAfterRun.Load() {
		t.Fatal("engine was not closed")
	}
}

func TestCloseIdempotentAndRejectsNewRuns(t *testing.T) {
	eng := &fakeEngine{}
	a := newTestAgent(Config{}, map[Language]engine{LanguagePython: eng})
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	res := a.Run(context.Background(), Request{Language: LanguagePython, Source: "x"})
	if res.Status != StatusInvalidRequest {
		t.Fatalf("run after close status = %s", res.Status)
	}
}

func TestCloseContextExpires(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	eng := &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
		close(started)
		<-release
		return engineResult{Status: StatusCompleted}
	}}
	a := newTestAgent(Config{}, map[Language]engine{LanguagePython: eng})
	go func() { a.Run(context.Background(), Request{Language: LanguagePython, Source: "x"}) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline exceeded", err)
	}
	close(release)
	// A later Close waits for the background shutdown.
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRunDurationIsMeasured(t *testing.T) {
	eng := &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
		time.Sleep(2 * time.Millisecond)
		return engineResult{Status: StatusCompleted}
	}}
	a := newTestAgent(Config{}, map[Language]engine{LanguagePython: eng})
	res := a.Run(context.Background(), Request{Language: LanguagePython, Source: "x"})
	if res.Duration <= 0 {
		t.Fatalf("Duration = %s", res.Duration)
	}
}

func TestRunMapsEngineResult(t *testing.T) {
	code := 3
	eng := &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
		return engineResult{
			Status:   StatusFailed,
			Value:    0,
			Output:   []Output{{Kind: "text", Stream: "stdout", Text: "hi"}},
			Error:    &Error{Name: "ProcessError", Message: "bad"},
			ExitCode: &code,
			Warnings: []string{"w"},
		}
	}}
	a := newTestAgent(Config{AllowProcess: true}, map[Language]engine{LanguageBash: eng})
	res := a.Run(context.Background(), Request{Language: LanguageBash, Source: "x", AllowProcess: true})
	if res.Status != StatusFailed || res.Error == nil || res.Error.Name != "ProcessError" {
		t.Fatalf("result = %+v", res)
	}
	if !res.HasValue() || res.Value != 0 {
		t.Fatalf("value = %v, has = %v", res.Value, res.HasValue())
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("exit code = %v", res.ExitCode)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestRunPreservesPresentNullValue(t *testing.T) {
	eng := &fakeEngine{runFn: func(ctx context.Context, req engineRequest) engineResult {
		return engineResult{Status: StatusCompleted, Value: nil, ValueSet: true}
	}}
	a := newTestAgent(Config{}, map[Language]engine{LanguageJavaScript: eng})
	res := a.Run(context.Background(), Request{Language: LanguageJavaScript, Source: "x"})
	if !res.HasValue() {
		t.Fatal("present null value was lost")
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"value":null`) {
		t.Fatalf("marshal = %s, want a present null value", data)
	}
}

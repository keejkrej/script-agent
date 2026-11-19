package scriptagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/keejkrej/quickpy/capability"
)

// Agent is a shared, concurrency-safe script execution coordinator bound to
// one explicit workspace root.
//
// An Agent owns one capability.Adapter and one engine per language. Python and
// JavaScript engines are initialized lazily on first use, so a Bash-only
// caller does not pay for a WASM compile. Close is serialized and waits for
// in-flight executions before releasing the engines and the workspace.
type Agent struct {
	cfg       Config
	workspace string
	adapter   *capability.Adapter
	engines   map[Language]engine

	sem      chan struct{}
	closedCh chan struct{}

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	closeMu   sync.Mutex
	closing   bool
	closeDone chan struct{}
	closeErr  error
}

// New validates cfg, opens the workspace capability, and returns an Agent.
//
// The workspace must be an existing directory. It is resolved to an absolute
// path once; later working-directory changes do not move the capability. New
// does not read the host environment, the current directory, or any ambient
// configuration.
func New(cfg Config) (*Agent, error) {
	resolved, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(resolved.Workspace)
	if err != nil {
		return nil, &ConfigError{Field: "Workspace", Message: fmt.Sprintf("resolve absolute path: %v", err)}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, &ConfigError{Field: "Workspace", Message: fmt.Sprintf("stat: %v", err)}
	}
	if !info.IsDir() {
		return nil, &ConfigError{Field: "Workspace", Message: "must be a directory"}
	}
	resolved.Workspace = abs

	adapter, err := capability.NewWorkspace(capability.WorkspaceOptions{
		Root:     abs,
		ReadOnly: !resolved.AllowWrites,
		MaxBytes: int64(resolved.MaxOutputBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("scriptagent: open workspace: %w", err)
	}

	a := &Agent{
		cfg:       resolved,
		workspace: abs,
		adapter:   adapter,
		engines: map[Language]engine{
			LanguagePython:     newPythonEngine(adapter, resolved),
			LanguageJavaScript: newJavaScriptEngine(adapter, resolved),
			LanguageBash:       newBashEngine(resolved),
		},
		sem:      make(chan struct{}, resolved.MaxExecutions),
		closedCh: make(chan struct{}),
	}
	return a, nil
}

// Workspace returns the absolute workspace root the Agent is confined to.
func (a *Agent) Workspace() string { return a.workspace }

// Config returns the resolved configuration. It is a copy.
func (a *Agent) Config() Config { return a.cfg }

// Run executes one request and returns its terminal Result. It never returns a
// Go error: invalid requests, denials, timeouts, cancellations, limits, and
// script failures are all Result statuses.
//
// Run is safe for concurrent use up to Config.MaxExecutions. A request that
// arrives when the Agent is at capacity waits for a slot or for ctx to end.
func (a *Agent) Run(ctx context.Context, req Request) Result {
	start := time.Now()
	res := a.run(ctx, req)
	res.Duration = time.Since(start)
	return res
}

func (a *Agent) run(ctx context.Context, req Request) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	lang := req.Language
	if !lang.Valid() {
		return invalidResult(lang, "language", fmt.Sprintf("unsupported language %q", lang))
	}
	if len(req.Source) > a.cfg.MaxSourceBytes {
		return invalidResult(lang, "source_too_large",
			fmt.Sprintf("source is %d bytes, limit is %d", len(req.Source), a.cfg.MaxSourceBytes))
	}
	if req.Timeout < 0 {
		return invalidResult(lang, "timeout", "Timeout must be >= 0")
	}
	timeout := a.cfg.Timeout
	if req.Timeout > 0 {
		if req.Timeout > a.cfg.Timeout {
			return invalidResult(lang, "timeout",
				fmt.Sprintf("Timeout %s exceeds the agent limit %s", req.Timeout, a.cfg.Timeout))
		}
		timeout = req.Timeout
	}
	if req.MaxOutputBytes < 0 {
		return invalidResult(lang, "max_output_bytes", "MaxOutputBytes must be >= 0")
	}
	maxOut := a.cfg.MaxOutputBytes
	if req.MaxOutputBytes > 0 {
		if req.MaxOutputBytes > a.cfg.MaxOutputBytes {
			return invalidResult(lang, "max_output_bytes",
				fmt.Sprintf("MaxOutputBytes %d exceeds the agent limit %d", req.MaxOutputBytes, a.cfg.MaxOutputBytes))
		}
		maxOut = req.MaxOutputBytes
	}
	if req.AllowProcess && lang != LanguageBash {
		return invalidResult(lang, "allow_process", "AllowProcess is only valid for bash")
	}
	if lang == LanguageBash && (!a.cfg.AllowProcess || !req.AllowProcess) {
		return deniedResult(lang, "process_denied",
			"process execution is not allowed; set Config.AllowProcess and Request.AllowProcess")
	}

	select {
	case a.sem <- struct{}{}:
	case <-a.closedCh:
		return invalidResult(lang, "closed", "agent is closed")
	case <-ctx.Done():
		return cancelledResult(lang)
	}
	defer func() { <-a.sem }()

	if !a.begin() {
		return invalidResult(lang, "closed", "agent is closed")
	}
	defer a.end()

	eng := a.engines[lang]
	if eng == nil {
		return unsupportedResult(lang, "language", "language is not available in this build")
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	er := eng.Run(runCtx, engineRequest{
		Source:         req.Source,
		Filename:       req.Filename,
		Timeout:        timeout,
		MaxOutputBytes: maxOut,
	})
	return resultFromEngine(lang, er)
}

// begin registers an in-flight execution. It is serialized with Close so a
// WaitGroup Add can never race a Wait.
func (a *Agent) begin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.wg.Add(1)
	return true
}

func (a *Agent) end() { a.wg.Done() }

// Close stops admission, waits for in-flight executions, and then closes the
// engines and the workspace capability. It is idempotent and safe for
// concurrent callers. A short context returns early; shutdown continues in the
// background and a later Close can wait for it.
func (a *Agent) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.closeMu.Lock()
	if !a.closing {
		a.closing = true
		a.mu.Lock()
		a.closed = true
		close(a.closedCh)
		a.mu.Unlock()
		a.closeDone = make(chan struct{})
		go a.shutdown()
	}
	done := a.closeDone
	a.closeMu.Unlock()

	select {
	case <-done:
		a.closeMu.Lock()
		defer a.closeMu.Unlock()
		return a.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) shutdown() {
	a.wg.Wait()
	var first error
	for _, lang := range []Language{LanguagePython, LanguageJavaScript, LanguageBash} {
		eng := a.engines[lang]
		if eng == nil {
			continue
		}
		if err := eng.Close(context.Background()); err != nil && first == nil {
			first = err
		}
	}
	if a.adapter != nil {
		if err := a.adapter.Close(); err != nil && first == nil {
			first = err
		}
	}
	a.closeMu.Lock()
	a.closeErr = first
	a.closeMu.Unlock()
	close(a.closeDone)
}

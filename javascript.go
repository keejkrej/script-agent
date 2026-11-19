package scriptagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/keejkrej/pi-go/codemode"
	codemoderuntime "github.com/keejkrej/pi-go/codemode/runtime"
	"github.com/keejkrej/quickpy/capability"
	"github.com/keejkrej/script-agent/adapter/quickjs"
)

// javascriptMemoryLimitBytes is the QuickJS VM memory ceiling. It matches the
// limit used by the adapter's own tests and is independent of the Python
// memory limit.
const javascriptMemoryLimitBytes = 256 << 20

// javascriptEngine runs JavaScript through the pi-go codemode QuickJS sandbox.
//
// The sandbox is created lazily on first use. It owns the tool registry and
// the trusted `fs` facade; every execution gets a fresh VM and fresh globals.
type javascriptEngine struct {
	adapter *capability.Adapter
	cfg     Config

	once sync.Once
	sb   *codemoderuntime.CodemodeSandbox
	err  error
}

func newJavaScriptEngine(adapter *capability.Adapter, cfg Config) *javascriptEngine {
	return &javascriptEngine{adapter: adapter, cfg: cfg}
}

func (j *javascriptEngine) init() error {
	j.once.Do(func() {
		timeoutMs := float64(j.cfg.Timeout / time.Millisecond)
		if timeoutMs < 1 {
			timeoutMs = 1
		}
		memory := javascriptMemoryLimitBytes
		maxOut := j.cfg.MaxOutputBytes
		sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
			TimeoutMs:            &timeoutMs,
			MemoryLimitBytes:     &memory,
			MaxOutputBytes:       &maxOut,
			TrustedPreludeSource: quickjs.FacadeSource,
		})
		if err != nil {
			j.err = fmt.Errorf("scriptagent: javascript sandbox: %w", err)
			return
		}
		if err := quickjs.Install(sb, j.adapter); err != nil {
			_ = sb.Close()
			j.err = fmt.Errorf("scriptagent: install javascript capabilities: %w", err)
			return
		}
		j.sb = sb
	})
	return j.err
}

func (j *javascriptEngine) Run(ctx context.Context, req engineRequest) engineResult {
	if err := j.init(); err != nil {
		return engineResult{
			Status: StatusUnsupported,
			Error:  &Error{Name: "Unsupported", Code: "javascript_unavailable", Message: err.Error()},
		}
	}
	timeoutMs := float64(req.Timeout / time.Millisecond)
	if timeoutMs < 1 {
		timeoutMs = 1
	}
	res, err := j.sb.Execute(ctx, req.Source, &codemode.CodemodeExecuteOptions{TimeoutMs: &timeoutMs})
	if err != nil {
		return engineResult{
			Status: StatusFailed,
			Error:  &Error{Name: "EngineError", Message: err.Error()},
		}
	}
	warnings := []string{"JavaScript runs in a QuickJS sandbox, not Node.js: there is no module loader, npm, or network access."}
	switch r := res.(type) {
	case *codemode.CodemodeSuccess:
		out, limited := limitOutput(mapCodemodeOutputs(r.Output), req.MaxOutputBytes)
		er := engineResult{
			Status:   StatusCompleted,
			Output:   out,
			Warnings: warnings,
		}
		if limited {
			er.Status = StatusLimitExceeded
			er.Error = &Error{Name: "LimitExceeded", Message: "javascript output exceeded the limit"}
		}
		if r.Value.IsSome() {
			v, _ := r.Value.Get()
			er.Value = v
			er.ValueSet = true
		} else if r.Value.IsNull() {
			er.Value = nil
			er.ValueSet = true
		}
		return er
	case *codemode.CodemodeFailure:
		status, e := mapCodemodeError(r.Error)
		out, _ := limitOutput(mapCodemodeOutputs(r.Output), req.MaxOutputBytes)
		return engineResult{
			Status:   status,
			Output:   out,
			Error:    e,
			Warnings: warnings,
		}
	default:
		return engineResult{
			Status:   StatusFailed,
			Error:    &Error{Name: "EngineError", Message: "unexpected codemode result"},
			Warnings: warnings,
		}
	}
}

func (j *javascriptEngine) Close(ctx context.Context) error {
	if j.sb == nil {
		return nil
	}
	return j.sb.Close()
}

func mapCodemodeError(e codemode.CodemodeError) (Status, *Error) {
	out := &Error{Name: "EngineError", Message: e.Message}
	if e.Name != nil && *e.Name != "" {
		out.Name = *e.Name
	}
	if e.Stack != nil {
		out.Stack = *e.Stack
	}
	switch e.Kind {
	case codemode.CodemodeErrorKindScript:
		if out.Name == "EngineError" {
			out.Name = "ScriptError"
		}
		return StatusFailed, out
	case codemode.CodemodeErrorKindTimeout:
		out.Name = "Timeout"
		return StatusTimedOut, out
	case codemode.CodemodeErrorKindAborted:
		out.Name = "Cancelled"
		return StatusCancelled, out
	case codemode.CodemodeErrorKindLimit:
		out.Name = "LimitExceeded"
		return StatusLimitExceeded, out
	case codemode.CodemodeErrorKindSandbox:
		out.Name = "EngineError"
		return StatusFailed, out
	default:
		return StatusFailed, out
	}
}

func mapCodemodeOutputs(items codemode.CodemodeOutputItemList) []Output {
	var out []Output
	for _, item := range items {
		switch o := item.(type) {
		case *codemode.CodemodeTextOutput:
			if o.Text == "" {
				continue
			}
			out = append(out, Output{Kind: "text", Stream: "stdout", Text: o.Text})
		case *codemode.CodemodeImageOutput:
			data, err := base64.StdEncoding.DecodeString(o.Data)
			if err != nil {
				continue
			}
			out = append(out, Output{Kind: "image", Stream: "stdout", MIME: o.MimeType, Data: data})
		}
	}
	return out
}

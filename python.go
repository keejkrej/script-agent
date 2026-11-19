package scriptagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/keejkrej/quickpy"
	"github.com/keejkrej/quickpy/capability"
	"github.com/keejkrej/quickpy/capability/quickpybridge"
	"github.com/keejkrej/script-agent/internal/artifacts"
)

// pythonEngine runs Python through the QuickPy WASM guest.
//
// The runtime is created lazily on first use. It owns one compiled artifact
// and a tool registry; every execution gets a fresh guest instance and fresh
// globals. The workspace capability is installed once, before the first
// execution.
type pythonEngine struct {
	adapter *capability.Adapter
	cfg     Config

	once sync.Once
	rt   *quickpy.Runtime
	err  error
}

func newPythonEngine(adapter *capability.Adapter, cfg Config) *pythonEngine {
	return &pythonEngine{adapter: adapter, cfg: cfg}
}

func (p *pythonEngine) init() error {
	p.once.Do(func() {
		if err := artifacts.VerifyQuickPy(); err != nil {
			p.err = fmt.Errorf("scriptagent: python artifact: %w", err)
			return
		}
		meta := artifacts.QuickPyMetadata()
		if meta.ABI != 4 {
			p.err = fmt.Errorf("scriptagent: python artifact ABI %d, want 4", meta.ABI)
			return
		}
		rt, err := quickpy.New(quickpy.RuntimeOptions{
			Artifact:       quickpy.ArtifactSource{Bytes: artifacts.QuickPyWASM()},
			MaxMemoryBytes: p.cfg.MaxPythonMemoryBytes,
			MaxExecutions:  p.cfg.MaxExecutions,
			DefaultTimeout: p.cfg.Timeout,
			MaxSourceBytes: p.cfg.MaxSourceBytes,
			MaxOutputBytes: p.cfg.MaxOutputBytes,
		})
		if err != nil {
			p.err = fmt.Errorf("scriptagent: python runtime: %w", err)
			return
		}
		if err := quickpybridge.Install(rt, p.adapter); err != nil {
			_ = rt.Close(context.Background())
			p.err = fmt.Errorf("scriptagent: install python capabilities: %w", err)
			return
		}
		p.rt = rt
	})
	return p.err
}

func (p *pythonEngine) Run(ctx context.Context, req engineRequest) engineResult {
	if err := p.init(); err != nil {
		return engineResult{
			Status: StatusUnsupported,
			Error:  &Error{Name: "Unsupported", Code: "python_unavailable", Message: err.Error()},
		}
	}
	filename := req.Filename
	if filename == "" {
		filename = "main.py"
	}
	exec, err := p.rt.Execute(ctx, quickpy.ExecuteRequest{
		Source:         req.Source,
		Filename:       filename,
		Timeout:        req.Timeout,
		MaxOutputBytes: req.MaxOutputBytes,
	})
	if err != nil {
		status, e := mapQuickpyError(err)
		return engineResult{Status: status, Error: e}
	}

	// Wait with a hard cap that is independent of the caller's context. The
	// runtime cancels the execution when the caller's context ends, so this
	// normally returns immediately; the cap only protects against a stuck
	// engine.
	waitCtx, cancel := context.WithTimeout(context.Background(), req.Timeout+5*time.Second)
	defer cancel()
	res, waitErr := exec.Wait(waitCtx)
	out := mapQuickpyOutputs(res.Outputs)
	if waitErr != nil {
		if errors.Is(waitErr, quickpy.ErrWaitTimeout) {
			return engineResult{
				Status: StatusTimedOut,
				Output: out,
				Error:  &Error{Name: "Timeout", Message: "python execution did not finish before the wait deadline"},
			}
		}
		status, e := mapQuickpyError(waitErr)
		return engineResult{Status: status, Output: out, Error: e}
	}

	er := engineResult{
		Status: mapQuickpyStatus(res.Status),
		Output: out,
	}
	if res.StateWritten {
		er.Value = res.State
		er.ValueSet = true
	}
	return er
}

func (p *pythonEngine) Close(ctx context.Context) error {
	if p.rt == nil {
		return nil
	}
	return p.rt.Close(ctx)
}

func mapQuickpyStatus(s quickpy.Status) Status {
	switch s {
	case quickpy.StatusCompleted:
		return StatusCompleted
	case quickpy.StatusFailed:
		return StatusFailed
	case quickpy.StatusTimedOut:
		return StatusTimedOut
	case quickpy.StatusCancelled:
		return StatusCancelled
	case quickpy.StatusLimitExceeded:
		return StatusLimitExceeded
	case quickpy.StatusStalled:
		return StatusStalled
	default:
		return StatusFailed
	}
}

func mapQuickpyErrorStatus(qe *quickpy.Error) Status {
	switch qe.Kind {
	case quickpy.KindTimedOut:
		return StatusTimedOut
	case quickpy.KindCancelled:
		return StatusCancelled
	case quickpy.KindLimitExceeded, quickpy.KindCapacityExceeded:
		return StatusLimitExceeded
	case quickpy.KindStalled:
		return StatusStalled
	case quickpy.KindInvalidRequest:
		return StatusInvalidRequest
	default:
		return StatusFailed
	}
}

func mapQuickpyError(err error) (Status, *Error) {
	var qe *quickpy.Error
	if !errors.As(err, &qe) {
		return StatusFailed, &Error{Name: "EngineError", Message: err.Error()}
	}
	status := mapQuickpyErrorStatus(qe)
	e := &Error{Name: "EngineError", Code: string(qe.Kind), Message: qe.Message}
	switch qe.Kind {
	case quickpy.KindScriptError:
		e.Name = "ScriptError"
		if qe.Script != nil {
			if qe.Script.Type != "" {
				e.Code = qe.Script.Type
			}
			if qe.Script.Message != "" {
				e.Message = qe.Script.Message
			}
			e.Stack = qe.Script.Traceback
		}
		if qe.Tool != nil {
			if qe.Tool.Code != "" {
				e.Code = qe.Tool.Code
			}
			if qe.Tool.Message != "" {
				e.Message = qe.Tool.Message
			}
		}
	case quickpy.KindTimedOut:
		e.Name = "Timeout"
	case quickpy.KindCancelled:
		e.Name = "Cancelled"
	case quickpy.KindLimitExceeded:
		e.Name = "LimitExceeded"
	case quickpy.KindStalled:
		e.Name = "Stalled"
	case quickpy.KindInvalidRequest:
		e.Name = "InvalidRequest"
	case quickpy.KindCapacityExceeded:
		e.Name = "LimitExceeded"
	}
	if e.Message == "" && qe.Cause != nil {
		e.Message = qe.Cause.Error()
	}
	return status, e
}

func mapQuickpyOutputs(events []quickpy.Event) []Output {
	var out []Output
	for _, ev := range events {
		switch e := ev.(type) {
		case quickpy.TextEvent:
			if e.Text == "" {
				continue
			}
			out = append(out, Output{Kind: "text", Stream: "stdout", Text: e.Text})
		case quickpy.ConsoleEvent:
			if e.Text == "" {
				continue
			}
			out = append(out, Output{Kind: "text", Stream: "stderr", Level: e.Level, Text: e.Text})
		case quickpy.ImageEvent:
			out = append(out, Output{
				Kind:   "image",
				Stream: "stdout",
				MIME:   e.MIME,
				Data:   append([]byte(nil), e.Data...),
			})
		}
	}
	return out
}

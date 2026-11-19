// Package quickjs installs a capability.Adapter's filesystem operations
// as codemode tools and provides a trusted prelude that exposes a small,
// frozen `fs` namespace to guest JavaScript.
//
// The bridge is deliberately thin: it translates a codemode tool call into the
// adapter's (operation, map[string]any) contract, forwards the caller's
// context unchanged, and converts a *capability.Error into an error that
// implements Code() string so the codemode runtime can attach the stable
// capability code to the guest's rejected Promise. It does not choose a
// workspace, grant permissions, or add ambient filesystem access. The
// application must construct the adapter with its own policy, call Install
// before Execute, and set CodemodeSandboxOptions.TrustedPreludeSource to
// FacadeSource.
package quickjs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/keejkrej/pi-go/codemode"
	"github.com/keejkrej/quickpy/capability"
)

// ToolRegistrar is the subset of *runtime.CodemodeSandbox that Install needs.
// *runtime.CodemodeSandbox satisfies it.
type ToolRegistrar interface {
	RegisterTool(tool *codemode.CodemodeTool) error
	UnregisterTool(name string) bool
}

// adapter is the capability surface Install needs. *capability.Adapter
// implements it; tests use a stub.
type adapter interface {
	Handle(ctx context.Context, operation string, args map[string]any) (any, error)
	Operations() []string
}

// codedError carries a stable capability code to the codemode runtime. The
// runtime recognizes interface{ Code() string } and attaches the code to the
// guest's rejected Error.
type codedError struct {
	code    string
	message string
}

func (e *codedError) Error() string { return e.message }
func (e *codedError) Code() string  { return e.code }

// Install registers every operation exposed by a as a codemode tool.
//
// Install must be called before Execute. Execute snapshots the tool registry,
// so a tool installed after an execution starts is not visible to that
// execution.
//
// Registration is all-or-nothing. If a later registration fails, Install
// removes only the tools it registered and returns the original error. It
// never overwrites an existing tool: RegisterTool reports a duplicate, and
// Install rolls back the tools it added.
func Install(r ToolRegistrar, a *capability.Adapter) error {
	if a == nil {
		return errors.New("quickjs: adapter is nil")
	}
	return install(r, a)
}

func install(r ToolRegistrar, a adapter) error {
	if r == nil {
		return errors.New("quickjs: registrar is nil")
	}
	if a == nil {
		return errors.New("quickjs: adapter is nil")
	}

	ops := append([]string(nil), a.Operations()...)
	sort.Strings(ops)

	installed := make([]string, 0, len(ops))
	for _, op := range ops {
		tool := &codemode.CodemodeTool{
			Name:        op,
			Description: toolDescription(op),
			Execute:     handler(a, op),
		}
		if err := r.RegisterTool(tool); err != nil {
			if rbErr := rollback(r, installed); rbErr != nil {
				return fmt.Errorf("quickjs: register %q: %w (rollback failed: %v)", op, err, rbErr)
			}
			return fmt.Errorf("quickjs: register %q: %w", op, err)
		}
		installed = append(installed, op)
	}
	return nil
}

func rollback(r ToolRegistrar, installed []string) error {
	var failed []string
	for i := len(installed) - 1; i >= 0; i-- {
		if !r.UnregisterTool(installed[i]) {
			failed = append(failed, installed[i])
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not unregister %v", failed)
	}
	return nil
}

func toolDescription(op string) *string {
	desc, ok := toolDescriptions[op]
	if !ok {
		return nil
	}
	return &desc
}

var toolDescriptions = map[string]string{
	capability.OpReadText:   "Read a UTF-8 text file from the workspace.",
	capability.OpWriteText:  "Write a UTF-8 text file to the workspace.",
	capability.OpReadBytes:  "Read a file from the workspace as base64-encoded bytes.",
	capability.OpWriteBytes: "Write base64-encoded bytes to a workspace file.",
	capability.OpExists:     "Report whether a workspace path exists.",
	capability.OpListDir:    "List a workspace directory.",
	capability.OpMkdir:      "Create a workspace directory.",
	capability.OpRemove:     "Remove a workspace file, symlink, or empty directory.",
}

func handler(a adapter, op string) func(ctx context.Context, args any) (any, error) {
	return func(ctx context.Context, args any) (any, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		m, err := toArgs(args)
		if err != nil {
			return nil, &codedError{code: capability.CodeToolArgument, message: err.Error()}
		}
		result, err := a.Handle(ctx, op, m)
		if err != nil {
			// The adapter reports cancellation as a structured io_error. The
			// runtime classifies context.Canceled/DeadlineExceeded specially,
			// so preserve the context cause instead of flattening it.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, convertError(err)
		}
		return result, nil
	}
}

// toArgs converts the JSON value the runtime decoded from the guest into the
// map[string]any the capability contract expects. encoding/json is used
// deliberately: the runtime's jsonx values are not part of this package's
// contract, and a JSON round trip is the same normalization the guest saw.
func toArgs(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("tool arguments must be a JSON object: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// convertError preserves the adapter's stable code and message. Unknown
// errors are returned unchanged so the runtime treats them as ordinary tool
// failures rather than host faults.
func convertError(err error) error {
	var ce *capability.Error
	if errors.As(err, &ce) {
		return &codedError{code: ce.Code, message: ce.Message}
	}
	return err
}

package quickjs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keejkrej/pi-go/codemode"
	"github.com/keejkrej/quickpy/capability"
)

// Compile-time checks that the real types satisfy the bridge's small
// interfaces.
var (
	_ ToolRegistrar = (*fakeRegistrar)(nil)
	_ adapter       = (*capability.Adapter)(nil)
)

type fakeAdapter struct {
	ops    []string
	handle func(ctx context.Context, operation string, args map[string]any) (any, error)
}

func (f *fakeAdapter) Operations() []string { return f.ops }

func (f *fakeAdapter) Handle(ctx context.Context, operation string, args map[string]any) (any, error) {
	if f.handle != nil {
		return f.handle(ctx, operation, args)
	}
	return nil, nil
}

type fakeRegistrar struct {
	tools        map[string]*codemode.CodemodeTool
	order        []string
	failAt       int // 1-based registration attempt that fails; 0 means never
	unregistered []string
}

func (f *fakeRegistrar) RegisterTool(tool *codemode.CodemodeTool) error {
	if f.failAt > 0 && len(f.order)+1 == f.failAt {
		return errors.New("register failed")
	}
	if f.tools == nil {
		f.tools = make(map[string]*codemode.CodemodeTool)
	}
	if _, ok := f.tools[tool.Name]; ok {
		return fmt.Errorf("tool %q already registered", tool.Name)
	}
	f.tools[tool.Name] = tool
	f.order = append(f.order, tool.Name)
	return nil
}

func (f *fakeRegistrar) UnregisterTool(name string) bool {
	f.unregistered = append(f.unregistered, name)
	if _, ok := f.tools[name]; !ok {
		return false
	}
	delete(f.tools, name)
	return true
}

func TestInstallRegistersSortedOperations(t *testing.T) {
	reg := &fakeRegistrar{}
	a := &fakeAdapter{ops: []string{"fs_write_text", "fs_read_text", "fs_exists"}}
	if err := install(reg, a); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := []string{"fs_exists", "fs_read_text", "fs_write_text"}
	if !reflect.DeepEqual(reg.order, want) {
		t.Fatalf("registered %v, want %v", reg.order, want)
	}
	for _, name := range want {
		if reg.tools[name] == nil || reg.tools[name].Execute == nil {
			t.Fatalf("tool %q is missing or has no Execute", name)
		}
	}
}

func TestInstallNilAdapter(t *testing.T) {
	if err := Install(&fakeRegistrar{}, nil); err == nil {
		t.Fatal("Install(nil adapter) succeeded")
	}
}

func TestInstallNilRegistrar(t *testing.T) {
	if err := install(nil, &fakeAdapter{}); err == nil {
		t.Fatal("install(nil registrar) succeeded")
	}
}

func TestInstallRollsBackOnFailure(t *testing.T) {
	reg := &fakeRegistrar{failAt: 2}
	a := &fakeAdapter{ops: []string{"fs_read_text", "fs_write_text", "fs_exists"}}
	err := install(reg, a)
	if err == nil {
		t.Fatal("install succeeded despite registration failure")
	}
	if !strings.Contains(err.Error(), "register failed") {
		t.Fatalf("error = %v", err)
	}
	// Sorted order is fs_exists, fs_read_text, fs_write_text. The second
	// registration fails, so only fs_exists was installed and rolled back.
	if !reflect.DeepEqual(reg.unregistered, []string{"fs_exists"}) {
		t.Fatalf("unregistered = %v", reg.unregistered)
	}
	if len(reg.tools) != 0 {
		t.Fatalf("tools left after rollback: %v", reg.tools)
	}
}

func TestInstallDoesNotOverwriteExistingTool(t *testing.T) {
	reg := &fakeRegistrar{}
	existing := &codemode.CodemodeTool{Name: "fs_read_text"}
	if err := reg.RegisterTool(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := &fakeAdapter{ops: []string{"fs_read_text", "fs_write_text"}}
	err := install(reg, a)
	if err == nil {
		t.Fatal("install succeeded despite duplicate tool")
	}
	if reg.tools["fs_read_text"] != existing {
		t.Fatal("existing tool was overwritten")
	}
	if len(reg.unregistered) != 0 {
		t.Fatalf("unregistered = %v, want none", reg.unregistered)
	}
}

func TestInstallRollbackLeavesPreExistingTools(t *testing.T) {
	reg := &fakeRegistrar{}
	existing := &codemode.CodemodeTool{Name: "fs_exists"}
	if err := reg.RegisterTool(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The seed counts as the first registration, so failAt 3 fails the second
	// tool Install adds.
	reg.failAt = 3
	a := &fakeAdapter{ops: []string{"fs_read_text", "fs_write_text"}}
	err := install(reg, a)
	if err == nil {
		t.Fatal("install succeeded despite registration failure")
	}
	if reg.tools["fs_exists"] != existing {
		t.Fatal("pre-existing tool was removed")
	}
	if !reflect.DeepEqual(reg.unregistered, []string{"fs_read_text"}) {
		t.Fatalf("unregistered = %v", reg.unregistered)
	}
}

func TestInstallRollbackFailureIsReported(t *testing.T) {
	reg := &fakeRegistrar{failAt: 2}
	a := &fakeAdapter{ops: []string{"fs_read_text", "fs_write_text"}}
	// Install fs_read_text, then fail. To force a rollback failure, wrap the
	// registrar so UnregisterTool always reports false.
	wrapped := &failingUnregister{fakeRegistrar: reg}
	err := install(wrapped, a)
	if err == nil {
		t.Fatal("install succeeded despite registration failure")
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("error = %v", err)
	}
}

type failingUnregister struct {
	*fakeRegistrar
}

func (f *failingUnregister) UnregisterTool(string) bool { return false }

func TestHandlerConvertsJSONArgs(t *testing.T) {
	var gotOp string
	var gotArgs map[string]any
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(_ context.Context, op string, args map[string]any) (any, error) {
			gotOp = op
			gotArgs = args
			return map[string]any{"text": "hello"}, nil
		},
	}
	h := handler(a, "fs_read_text")
	var args any
	if err := json.Unmarshal([]byte(`{"path":"a.txt","encoding":"utf-8"}`), &args); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	result, err := h(context.Background(), args)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gotOp != "fs_read_text" {
		t.Fatalf("operation = %q", gotOp)
	}
	if !reflect.DeepEqual(gotArgs, map[string]any{"path": "a.txt", "encoding": "utf-8"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
	if !reflect.DeepEqual(result, map[string]any{"text": "hello"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestHandlerNilArgsBecomesEmptyObject(t *testing.T) {
	var gotArgs map[string]any
	a := &fakeAdapter{
		ops: []string{"fs_exists"},
		handle: func(_ context.Context, _ string, args map[string]any) (any, error) {
			gotArgs = args
			return map[string]any{"exists": false}, nil
		},
	}
	h := handler(a, "fs_exists")
	if _, err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gotArgs == nil || len(gotArgs) != 0 {
		t.Fatalf("args = %#v, want empty non-nil map", gotArgs)
	}
}

func TestHandlerRejectsNonObjectArgs(t *testing.T) {
	called := false
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			called = true
			return nil, nil
		},
	}
	h := handler(a, "fs_read_text")
	_, err := h(context.Background(), []any{"not", "an", "object"})
	if called {
		t.Fatal("adapter was called for non-object args")
	}
	var coder interface{ Code() string }
	if !errors.As(err, &coder) {
		t.Fatalf("error = %T %v, want a coded error", err, err)
	}
	if coder.Code() != capability.CodeToolArgument {
		t.Fatalf("code = %q, want %q", coder.Code(), capability.CodeToolArgument)
	}
	if !strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestHandlerMapsCapabilityError(t *testing.T) {
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			return nil, &capability.Error{Code: capability.CodeNotFound, Message: "missing"}
		},
	}
	h := handler(a, "fs_read_text")
	_, err := h(context.Background(), nil)
	var coder interface{ Code() string }
	if !errors.As(err, &coder) {
		t.Fatalf("error = %T %v, want a coded error", err, err)
	}
	if coder.Code() != capability.CodeNotFound {
		t.Fatalf("code = %q, want %q", coder.Code(), capability.CodeNotFound)
	}
	if err.Error() != "missing" {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestHandlerMapsWrappedCapabilityError(t *testing.T) {
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			return nil, fmt.Errorf("wrapped: %w", &capability.Error{Code: capability.CodePermissionDenied, Message: "denied"})
		},
	}
	h := handler(a, "fs_read_text")
	_, err := h(context.Background(), nil)
	var coder interface{ Code() string }
	if !errors.As(err, &coder) {
		t.Fatalf("error = %T %v, want a coded error", err, err)
	}
	if coder.Code() != capability.CodePermissionDenied {
		t.Fatalf("code = %q", coder.Code())
	}
}

func TestHandlerUnknownErrorUnchanged(t *testing.T) {
	boom := errors.New("boom")
	a := &fakeAdapter{
		ops: []string{"fs_exists"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			return nil, boom
		},
	}
	h := handler(a, "fs_exists")
	_, err := h(context.Background(), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want original boom", err)
	}
	var coder interface{ Code() string }
	if errors.As(err, &coder) {
		t.Fatalf("unknown error was coded: %#v", coder)
	}
}

func TestHandlerPassesContext(t *testing.T) {
	type key struct{}
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "value"), deadline)
	defer cancel()

	a := &fakeAdapter{
		ops: []string{"fs_exists"},
		handle: func(got context.Context, _ string, _ map[string]any) (any, error) {
			if got.Value(key{}) != "value" {
				t.Errorf("context value not forwarded")
			}
			gotDeadline, ok := got.Deadline()
			if !ok || !gotDeadline.Equal(deadline) {
				t.Errorf("deadline = %v, %v; want %v", gotDeadline, ok, deadline)
			}
			return map[string]any{"exists": true}, nil
		},
	}
	h := handler(a, "fs_exists")
	if _, err := h(ctx, nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

func TestHandlerPreservesContextCancellation(t *testing.T) {
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			return nil, &capability.Error{Code: capability.CodeIOError, Message: "operation cancelled"}
		},
	}
	h := handler(a, "fs_read_text")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestHandlerPreservesContextDeadline(t *testing.T) {
	a := &fakeAdapter{
		ops: []string{"fs_read_text"},
		handle: func(context.Context, string, map[string]any) (any, error) {
			return nil, &capability.Error{Code: capability.CodeIOError, Message: "operation deadline exceeded"}
		},
	}
	h := handler(a, "fs_read_text")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := h(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

package runtime_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keejkrej/pi-go/codemode"
	codemoderuntime "github.com/keejkrej/pi-go/codemode/runtime"
)

// tlimNewSandbox builds a sandbox with an optional output budget.
func tlimNewSandbox(t *testing.T, tools []*codemode.CodemodeTool, maxOutput *int) *codemoderuntime.CodemodeSandbox {
	t.Helper()
	sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
		Tools:          tools,
		TimeoutMs:      tsandFloatPtr(10_000),
		MaxOutputBytes: maxOutput,
	})
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func tlimTexts(items codemode.CodemodeOutputItemList) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(*codemode.CodemodeTextOutput); ok {
			out = append(out, text.Text)
		}
	}
	return out
}

func tlimTextBytes(items codemode.CodemodeOutputItemList) int {
	n := 0
	for _, item := range items {
		if text, ok := item.(*codemode.CodemodeTextOutput); ok {
			n += len(text.Text)
		}
	}
	return n
}

// TestOutputLimitUnsetPreservesUnlimited is the baseline: a nil budget keeps
// the original unlimited behavior and every event is retained.
func TestOutputLimitUnsetPreservesUnlimited(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, nil)
	s := tsandExecSuccess(t, sb, ctx, `
		text("hello");
		console.log("world");
		text("!");
		return "ok";
	`, nil)
	if len(s.Output) != 3 {
		t.Fatalf("output = %+v", s.Output)
	}
	if got, want := strings.Join(tlimTexts(s.Output), "|"), "hello|world|!"; got != want {
		t.Fatalf("texts = %q, want %q", got, want)
	}
}

// TestOutputLimitTextUTF8 covers UTF-8 byte accounting and clipping at a rune
// boundary. "héllo" is 6 bytes; a 5-byte budget retains "héll".
func TestOutputLimitTextUTF8(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(5))
	f := tsandExecFailure(t, sb, ctx, `text("héllo"); return 1;`, nil)
	if f.Error.Kind != codemode.CodemodeErrorKindLimit {
		t.Fatalf("kind = %q, message = %q", f.Error.Kind, f.Error.Message)
	}
	if !strings.Contains(f.Error.Message, "Output limit exceeded") {
		t.Fatalf("message = %q", f.Error.Message)
	}
	if len(f.Output) != 1 {
		t.Fatalf("output = %+v", f.Output)
	}
	text := f.Output[0].(*codemode.CodemodeTextOutput)
	if text.Text != "héll" {
		t.Fatalf("text = %q, want %q", text.Text, "héll")
	}
	if len(text.Text) != 5 {
		t.Fatalf("text bytes = %d, want 5", len(text.Text))
	}
}

// TestOutputLimitExactCapSucceeds checks that an item that exactly fills the
// budget is retained and the run still succeeds.
func TestOutputLimitExactCapSucceeds(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(4))
	s := tsandExecSuccess(t, sb, ctx, `text("AAAA"); return 1;`, nil)
	if len(s.Output) != 1 || tlimTextBytes(s.Output) != 4 {
		t.Fatalf("output = %+v", s.Output)
	}
}

// TestOutputLimitImageDecodedBytes checks that image output counts decoded
// bytes, not base64 characters. "iVBORw0KGgo=" is 12 base64 characters but 8
// decoded bytes, so an 8-byte budget fits it exactly.
func TestOutputLimitImageDecodedBytes(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(8))
	s := tsandExecSuccess(t, sb, ctx, `image("data:image/png;base64,iVBORw0KGgo="); return 1;`, nil)
	if len(s.Output) != 1 {
		t.Fatalf("output = %+v", s.Output)
	}
	img, ok := s.Output[0].(*codemode.CodemodeImageOutput)
	if !ok {
		t.Fatalf("output[0] = %T", s.Output[0])
	}
	if img.Data != "iVBORw0KGgo=" {
		t.Fatalf("data = %q", img.Data)
	}
}

// TestOutputLimitImageClipped checks that an oversized image is clipped to a
// raw byte prefix and re-encoded, and the run fails with kind "limit".
func TestOutputLimitImageClipped(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(4))
	f := tsandExecFailure(t, sb, ctx, `image("data:image/png;base64,iVBORw0KGgo="); return 1;`, nil)
	if f.Error.Kind != codemode.CodemodeErrorKindLimit {
		t.Fatalf("kind = %q, message = %q", f.Error.Kind, f.Error.Message)
	}
	if len(f.Output) != 1 {
		t.Fatalf("output = %+v", f.Output)
	}
	img, ok := f.Output[0].(*codemode.CodemodeImageOutput)
	if !ok {
		t.Fatalf("output[0] = %T", f.Output[0])
	}
	// First four decoded bytes are the PNG signature 89 50 4E 47.
	if img.Data != "iVBORw==" {
		t.Fatalf("data = %q, want %q", img.Data, "iVBORw==")
	}
}

// TestOutputLimitMultipleEventsPrefix checks that the retained prefix never
// exceeds the budget and that earlier output survives the failure.
func TestOutputLimitMultipleEventsPrefix(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(10))
	f := tsandExecFailure(t, sb, ctx, `
		text("AAAA");
		console.log("BB");
		text("CCCCCC");
		return 1;
	`, nil)
	if f.Error.Kind != codemode.CodemodeErrorKindLimit {
		t.Fatalf("kind = %q, message = %q", f.Error.Kind, f.Error.Message)
	}
	if got, want := strings.Join(tlimTexts(f.Output), "|"), "AAAA|BB|CCCC"; got != want {
		t.Fatalf("texts = %q, want %q", got, want)
	}
	if got := tlimTextBytes(f.Output); got != 10 {
		t.Fatalf("retained bytes = %d, want 10", got)
	}
}

// TestOutputLimitCancelsPendingTool checks that a pending tool call is
// cancelled when output exceeds the budget.
func TestOutputLimitCancelsPendingTool(t *testing.T) {
	ctx := context.Background()
	var started atomic.Bool
	var canceled atomic.Bool
	tool := &codemode.CodemodeTool{
		Name: "block",
		Execute: func(callCtx context.Context, _ any) (any, error) {
			started.Store(true)
			<-callCtx.Done()
			canceled.Store(true)
			return nil, callCtx.Err()
		},
	}
	sb := tlimNewSandbox(t, []*codemode.CodemodeTool{tool}, tsandIntPtr(4))
	f := tsandExecFailure(t, sb, ctx, `
		const pending = tools.block();
		text("AAAAA");
		await pending;
		return 1;
	`, nil)
	if f.Error.Kind != codemode.CodemodeErrorKindLimit {
		t.Fatalf("kind = %q, message = %q", f.Error.Kind, f.Error.Message)
	}
	if !started.Load() {
		t.Fatal("tool never started")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !canceled.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !canceled.Load() {
		t.Fatal("pending tool was not cancelled")
	}
	if len(f.Calls) != 1 || f.Calls[0].Status != codemode.CodemodeCallStatusCancelled {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

// TestOutputLimitFreshScopeSequential checks that each execution gets a fresh
// budget.
func TestOutputLimitFreshScopeSequential(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(4))
	for i := 0; i < 3; i++ {
		s := tsandExecSuccess(t, sb, ctx, `text("AAAA"); return 1;`, nil)
		if len(s.Output) != 1 || tlimTextBytes(s.Output) != 4 {
			t.Fatalf("execution %d: output = %+v", i, s.Output)
		}
	}
}

// TestOutputLimitFreshScopeConcurrent checks that concurrent executions do not
// share the byte counter.
func TestOutputLimitFreshScopeConcurrent(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(4))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := sb.Execute(ctx, `text("AAAA"); return 1;`, nil)
			if err != nil {
				t.Errorf("Execute: %v", err)
				return
			}
			success, ok := res.(*codemode.CodemodeSuccess)
			if !ok {
				t.Errorf("result = %T", res)
				return
			}
			if len(success.Output) != 1 || tlimTextBytes(success.Output) != 4 {
				t.Errorf("output = %+v", success.Output)
			}
		}()
	}
	wg.Wait()
}

// TestOutputLimitZeroBudgetIsUnlimited checks that a non-positive budget keeps
// the original unlimited behavior.
func TestOutputLimitZeroBudgetIsUnlimited(t *testing.T) {
	ctx := context.Background()
	sb := tlimNewSandbox(t, nil, tsandIntPtr(0))
	s := tsandExecSuccess(t, sb, ctx, `text("AAAA"); text("BBBB"); return 1;`, nil)
	if len(s.Output) != 2 {
		t.Fatalf("output = %+v", s.Output)
	}
}

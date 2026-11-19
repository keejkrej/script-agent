package scriptagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProbe attempts one workspace write and reports whether the capability
// adapter allowed it. It is used to prove the default posture without relying
// on the adapter's internal state.
const writeProbe = `import quickpy
async def main():
    try:
        await quickpy.fs.write_text("probe.txt", "hi")
        print("WROTE")
    except Exception as e:
        print("DENIED", e)
quickpy.run(main())
`

func outputText(out []Output) string {
	var b strings.Builder
	for _, o := range out {
		if o.Kind == "text" {
			b.WriteString(o.Text)
		}
	}
	return b.String()
}

// TestZeroConfigIsReadOnly proves that Config{Workspace: root} is read-only
// without any additional field. A write attempt must be denied by the
// capability adapter, not silently succeed.
func TestZeroConfigIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Config{Workspace: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := a.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	res := a.Run(context.Background(), Request{Language: LanguagePython, Source: writeProbe})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	text := outputText(res.Output)
	if !strings.Contains(text, "DENIED") {
		t.Fatalf("write was not denied: %q", text)
	}
	if strings.Contains(text, "WROTE") {
		t.Fatalf("write succeeded under the zero config: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe.txt")); !os.IsNotExist(err) {
		t.Fatalf("probe.txt exists after a denied write: %v", err)
	}
}

// TestAllowWritesPermitsWrites proves the explicit opt-in.
func TestAllowWritesPermitsWrites(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Config{Workspace: dir, AllowWrites: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := a.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	res := a.Run(context.Background(), Request{Language: LanguagePython, Source: writeProbe})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	text := outputText(res.Output)
	if !strings.Contains(text, "WROTE") {
		t.Fatalf("write was not permitted: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe.txt")); err != nil {
		t.Fatalf("probe.txt missing after an allowed write: %v", err)
	}
}

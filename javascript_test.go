package scriptagent

import (
	"encoding/base64"
	"testing"

	"github.com/keejkrej/pi-go/codemode"
)

func strPtr(s string) *string { return &s }

func TestMapCodemodeError(t *testing.T) {
	cases := []struct {
		name   string
		in     codemode.CodemodeError
		status Status
		want   string
	}{
		{
			name:   "script named",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindScript, Name: strPtr("TypeError"), Message: "bad", Stack: strPtr("at x")},
			status: StatusFailed,
			want:   "TypeError",
		},
		{
			name:   "script unnamed",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindScript, Message: "bad"},
			status: StatusFailed,
			want:   "ScriptError",
		},
		{
			name:   "timeout",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindTimeout, Message: "slow"},
			status: StatusTimedOut,
			want:   "Timeout",
		},
		{
			name:   "aborted",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindAborted, Message: "stop"},
			status: StatusCancelled,
			want:   "Cancelled",
		},
		{
			name:   "limit",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindLimit, Message: "big"},
			status: StatusLimitExceeded,
			want:   "LimitExceeded",
		},
		{
			name:   "sandbox",
			in:     codemode.CodemodeError{Kind: codemode.CodemodeErrorKindSandbox, Message: "trap"},
			status: StatusFailed,
			want:   "EngineError",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, e := mapCodemodeError(tc.in)
			if status != tc.status {
				t.Fatalf("status = %s, want %s", status, tc.status)
			}
			if e.Name != tc.want {
				t.Fatalf("name = %q, want %q", e.Name, tc.want)
			}
			if e.Message != tc.in.Message {
				t.Fatalf("message = %q", e.Message)
			}
		})
	}
}

func TestMapCodemodeOutputs(t *testing.T) {
	img := []byte{1, 2, 3}
	items := codemode.CodemodeOutputItemList{
		&codemode.CodemodeTextOutput{Type: "text", Text: "hi"},
		&codemode.CodemodeImageOutput{Type: "image", Data: base64.StdEncoding.EncodeToString(img), MimeType: "image/png"},
		&codemode.UnknownCodemodeOutputItem{},
	}
	out := mapCodemodeOutputs(items)
	if len(out) != 2 {
		t.Fatalf("outputs = %+v", out)
	}
	if out[0].Kind != "text" || out[0].Stream != "stdout" || out[0].Text != "hi" {
		t.Fatalf("text = %+v", out[0])
	}
	if out[1].Kind != "image" || out[1].MIME != "image/png" || len(out[1].Data) != 3 {
		t.Fatalf("image = %+v", out[1])
	}
}

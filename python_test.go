package scriptagent

import (
	"errors"
	"testing"

	"github.com/keejkrej/quickpy"
)

func TestMapQuickpyStatus(t *testing.T) {
	cases := map[quickpy.Status]Status{
		quickpy.StatusCompleted:     StatusCompleted,
		quickpy.StatusFailed:        StatusFailed,
		quickpy.StatusTimedOut:      StatusTimedOut,
		quickpy.StatusCancelled:     StatusCancelled,
		quickpy.StatusLimitExceeded: StatusLimitExceeded,
		quickpy.StatusStalled:       StatusStalled,
	}
	for in, want := range cases {
		if got := mapQuickpyStatus(in); got != want {
			t.Fatalf("mapQuickpyStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapQuickpyScriptError(t *testing.T) {
	err := &quickpy.Error{
		Kind:    quickpy.KindScriptError,
		Message: "script failed",
		Script: &quickpy.ScriptErrorInfo{
			Type:      "ValueError",
			Message:   "bad value",
			Traceback: "Traceback (most recent call last):\n  ...",
		},
	}
	status, e := mapQuickpyError(err)
	if status != StatusFailed {
		t.Fatalf("status = %s", status)
	}
	if e.Name != "ScriptError" || e.Code != "ValueError" || e.Message != "bad value" {
		t.Fatalf("error = %+v", e)
	}
	if e.Stack == "" {
		t.Fatal("missing stack")
	}
}

func TestMapQuickpyToolCause(t *testing.T) {
	err := &quickpy.Error{
		Kind: quickpy.KindScriptError,
		Tool: &quickpy.ToolCause{Tool: "fs_read_text", Code: "not_found", Message: "missing"},
	}
	status, e := mapQuickpyError(err)
	if status != StatusFailed {
		t.Fatalf("status = %s", status)
	}
	if e.Name != "ScriptError" || e.Code != "not_found" || e.Message != "missing" {
		t.Fatalf("error = %+v", e)
	}
}

func TestMapQuickpyErrorKinds(t *testing.T) {
	cases := []struct {
		kind   quickpy.Kind
		status Status
		name   string
	}{
		{quickpy.KindTimedOut, StatusTimedOut, "Timeout"},
		{quickpy.KindCancelled, StatusCancelled, "Cancelled"},
		{quickpy.KindLimitExceeded, StatusLimitExceeded, "LimitExceeded"},
		{quickpy.KindCapacityExceeded, StatusLimitExceeded, "LimitExceeded"},
		{quickpy.KindStalled, StatusStalled, "Stalled"},
		{quickpy.KindInvalidRequest, StatusInvalidRequest, "InvalidRequest"},
		{quickpy.KindGuestFault, StatusFailed, "EngineError"},
	}
	for _, tc := range cases {
		status, e := mapQuickpyError(&quickpy.Error{Kind: tc.kind, Message: "m"})
		if status != tc.status || e.Name != tc.name {
			t.Fatalf("%s: status=%s name=%s, want %s/%s", tc.kind, status, e.Name, tc.status, tc.name)
		}
	}
}

func TestMapQuickpyUnknownError(t *testing.T) {
	status, e := mapQuickpyError(errors.New("boom"))
	if status != StatusFailed || e.Name != "EngineError" || e.Message != "boom" {
		t.Fatalf("status=%s error=%+v", status, e)
	}
}

func TestMapQuickpyOutputs(t *testing.T) {
	events := []quickpy.Event{
		quickpy.TextEvent{Text: "out\n"},
		quickpy.ConsoleEvent{Level: "warn", Text: "careful"},
		quickpy.ImageEvent{MIME: "image/png", Data: []byte{1, 2, 3}},
		quickpy.ToolEvent{Phase: "start", Name: "fs_read_text"},
	}
	out := mapQuickpyOutputs(events)
	if len(out) != 3 {
		t.Fatalf("outputs = %+v", out)
	}
	if out[0].Kind != "text" || out[0].Stream != "stdout" || out[0].Text != "out\n" {
		t.Fatalf("text = %+v", out[0])
	}
	if out[1].Kind != "text" || out[1].Stream != "stderr" || out[1].Level != "warn" {
		t.Fatalf("console = %+v", out[1])
	}
	if out[2].Kind != "image" || out[2].MIME != "image/png" || len(out[2].Data) != 3 {
		t.Fatalf("image = %+v", out[2])
	}
}

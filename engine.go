package scriptagent

import (
	"context"
	"time"
	"unicode/utf8"
)

// engine is the narrow boundary between the coordinator and one language
// runtime. Tests inject fakes; the production implementations live in
// python.go, javascript.go, and bash.go.
type engine interface {
	// Run executes one request. It must not panic and must always return a
	// terminal engineResult. The coordinator owns the outer deadline and
	// capacity; the engine owns language-specific mapping.
	Run(ctx context.Context, req engineRequest) engineResult
	// Close releases the engine. It is called once, after all in-flight Run
	// calls have returned.
	Close(ctx context.Context) error
}

// engineRequest is the normalized request passed to an engine.
type engineRequest struct {
	Source         string
	Filename       string
	Timeout        time.Duration
	MaxOutputBytes int
}

// engineResult is the normalized result returned by an engine.
type engineResult struct {
	Status   Status
	Value    any
	ValueSet bool
	Output   []Output
	Error    *Error
	ExitCode *int
	Warnings []string
}

// resultFromEngine converts an engineResult into the public Result.
func resultFromEngine(lang Language, er engineResult) Result {
	status := er.Status
	if status == "" {
		status = StatusCompleted
	}
	res := Result{
		Language: lang,
		Status:   status,
		Output:   er.Output,
		Error:    er.Error,
		ExitCode: er.ExitCode,
		Warnings: er.Warnings,
	}
	if er.ValueSet || er.Value != nil {
		res.SetValue(er.Value)
	}
	return res
}

// invalidResult builds a StatusInvalidRequest result.
func invalidResult(lang Language, code, message string) Result {
	return Result{
		Language: lang,
		Status:   StatusInvalidRequest,
		Error:    &Error{Name: "InvalidRequest", Code: code, Message: message},
	}
}

// deniedResult builds a StatusDenied result.
func deniedResult(lang Language, code, message string) Result {
	return Result{
		Language: lang,
		Status:   StatusDenied,
		Error:    &Error{Name: "Denied", Code: code, Message: message},
	}
}

// unsupportedResult builds a StatusUnsupported result.
func unsupportedResult(lang Language, code, message string) Result {
	return Result{
		Language: lang,
		Status:   StatusUnsupported,
		Error:    &Error{Name: "Unsupported", Code: code, Message: message},
	}
}

// cancelledResult builds a StatusCancelled result.
func cancelledResult(lang Language) Result {
	return Result{
		Language: lang,
		Status:   StatusCancelled,
		Error:    &Error{Name: "Cancelled", Message: "execution cancelled"},
	}
}

// limitOutput clips output to max combined bytes. Text counts UTF-8 bytes and
// images count raw bytes. It returns the clipped output and whether the limit
// was hit.
func limitOutput(out []Output, max int) ([]Output, bool) {
	if max <= 0 {
		return out, false
	}
	total := 0
	for i, o := range out {
		n := len(o.Text)
		if o.Kind == "image" {
			n = len(o.Data)
		}
		if total+n > max {
			remaining := max - total
			if remaining < 0 {
				remaining = 0
			}
			if o.Kind == "image" {
				o.Data = o.Data[:remaining]
			} else {
				o.Text = clipUTF8(o.Text, remaining)
			}
			out[i] = o
			return out[:i+1], true
		}
		total += n
	}
	return out, false
}

// clipUTF8 returns the longest prefix of s that is at most max bytes and ends
// on a UTF-8 rune boundary.
func clipUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

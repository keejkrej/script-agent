package scriptagent

import (
	"context"
	"strings"

	"github.com/keejkrej/script-agent/process"
)

// runProcess is the production execFunc. It delegates to the process package,
// which owns process-group creation, timeout enforcement, and bounded output.
func runProcess(ctx context.Context, req execRequest) (execResult, error) {
	res, err := process.Run(ctx, process.Request{
		Argv:           req.Argv,
		Dir:            req.Dir,
		Env:            req.Env,
		Timeout:        req.Timeout,
		MaxStdoutBytes: req.MaxStdoutBytes,
		MaxStderrBytes: req.MaxStderrBytes,
	})
	return execResult{
		Stdout:         res.Stdout,
		Stderr:         res.Stderr,
		ExitCode:       res.ExitCode,
		TimedOut:       res.TimedOut,
		OutputExceeded: res.OutputExceeded,
	}, err
}

// isProcessUnsupported reports the process package's platform refusal. The
// sentinel is unexported, so the stable message is the only public signal.
func isProcessUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unsupported on this platform")
}

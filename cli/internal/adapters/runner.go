// Package adapters implements the side-effecting boundaries the core layer
// depends on (running commands, looking them up on PATH). Keeping these behind
// small interfaces/functions is what lets core be tested without touching the
// real system and lets CI drive the CLI deterministically.
package adapters

import (
	"bytes"
	"os/exec"
	"strings"
)

// ExecRunner runs commands with os/exec. It satisfies core.CommandRunner.
type ExecRunner struct{}

// Run executes name+args in dir and returns trimmed stdout.
func (ExecRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", &RunError{Err: err, Stderr: strings.TrimSpace(stderr.String())}
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunError carries a failed command's stderr so callers can surface it.
type RunError struct {
	Err    error
	Stderr string
}

func (e *RunError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Err.Error()
}

func (e *RunError) Unwrap() error { return e.Err }

// HasCommand reports whether name is on PATH.
func HasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

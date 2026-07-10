// Package adapters implements the side-effecting boundaries the core layer
// depends on (running commands, looking them up on PATH). Keeping these behind
// small interfaces/functions is what lets core be tested without touching the
// real system and lets CI drive the CLI deterministically.
package adapters

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
)

// ExecRunner runs commands with os/exec. It satisfies core.CommandRunner.
type ExecRunner struct{}

// Run executes name+args in dir and returns trimmed stdout.
func (r ExecRunner) Run(dir, name string, args ...string) (string, error) {
	return r.RunEnv(dir, nil, name, args...)
}

// RunEnv executes name+args in dir with extraEnv merged over the process
// environment (e.g. SOPS_AGE_KEY_FILE for sops/age), returning trimmed stdout.
func (ExecRunner) RunEnv(dir string, extraEnv map[string]string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = os.Environ()
		for k, v := range extraEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
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

// RunEnvInput is RunEnv with stdin fed from input. Use it for secret writes
// (sops set --value-stdin) so the value never appears in argv / a process list.
func (ExecRunner) RunEnvInput(dir string, extraEnv map[string]string, input string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = os.Environ()
		for k, v := range extraEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdin = strings.NewReader(input)
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

// Stream runs name+args in dir with extraEnv merged over the process
// environment, piping stdout/stderr straight to the terminal. Use it for
// long-running commands (nixos-rebuild) whose progress the operator should see
// live rather than buffered.
func (ExecRunner) Stream(dir string, extraEnv map[string]string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// HasCommand reports whether name is on PATH.
func HasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

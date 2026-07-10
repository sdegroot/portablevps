// Package secrets resolves secret *references* to their values at run time. A
// value may be a literal (returned unchanged), an `op://vault/item/field`
// 1Password reference (resolved with the `op` CLI), or an `env://VAR` reference
// (read from the environment). This lets the committed portablevps.toml and
// provider env files hold references instead of literal secrets: a human
// authenticates with 1Password, and CI passes literals via env:// — the same
// command works in both worlds.
//
// Ported from portablevps/scripts/portablevps_cloud/secrets.py so the two CLIs
// stay behaviourally aligned during the migration.
package secrets

import (
	"fmt"
	"strings"
)

// Exit codes mirror the Python resolver (sysexits-style).
const (
	exitUsage       = 64 // malformed reference
	exitUnavailable = 69 // the `op` CLI is not installed
	exitAPIFailure  = 70 // `op read` failed
)

// Error carries an exit code so the CLI layer can surface it faithfully.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// ExitCode reports the process exit code for a resolution failure.
func (e *Error) ExitCode() int { return e.Code }

// Runner runs the `op` CLI. The exec adapter satisfies it; tests inject a fake.
type Runner interface {
	Run(dir, name string, args ...string) (string, error)
}

// Resolver dereferences secret references.
type Resolver struct {
	Runner    Runner
	Getenv    func(string) string
	OpAccount string // optional; passed to `op` as --account when set
}

// IsReference reports whether s uses a known reference scheme.
func IsReference(s string) bool {
	return strings.HasPrefix(s, "op://") || strings.HasPrefix(s, "env://")
}

// Resolve returns the concrete value for a reference, or the input unchanged if
// it is a literal.
func (r Resolver) Resolve(value string) (string, error) {
	switch {
	case strings.HasPrefix(value, "op://"):
		return r.resolveOp(value)
	case strings.HasPrefix(value, "env://"):
		return r.resolveEnv(value)
	default:
		return value, nil
	}
}

// ResolveMapping resolves every reference-valued entry in m, leaving literals
// untouched. Returns a new map.
func (r Resolver) ResolveMapping(m map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		resolved, err := r.Resolve(v)
		if err != nil {
			return nil, err
		}
		out[k] = resolved
	}
	return out, nil
}

func (r Resolver) resolveOp(ref string) (string, error) {
	getenv := r.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	args := []string{"read"}
	account := r.OpAccount
	if account == "" {
		account = getenv("OP_ACCOUNT")
	}
	if account != "" {
		args = append(args, "--account", account)
	}
	args = append(args, ref)

	out, err := r.Runner.Run("", "op", args...)
	if err != nil {
		// A missing binary vs a failed read are different failure classes.
		if isNotFound(err) {
			return "", &Error{Code: exitUnavailable, Msg: fmt.Sprintf("1Password CLI (op) is required to resolve %q but was not found on PATH", ref)}
		}
		return "", &Error{Code: exitAPIFailure, Msg: fmt.Sprintf("op read %q failed: %v", ref, err)}
	}
	return strings.TrimRight(out, "\n"), nil
}

func (r Resolver) resolveEnv(ref string) (string, error) {
	name := strings.TrimPrefix(ref, "env://")
	if name == "" {
		return "", &Error{Code: exitUsage, Msg: "env:// reference is missing a variable name"}
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	value := getenv(name)
	if value == "" {
		return "", &Error{Code: exitUsage, Msg: fmt.Sprintf("env:// reference %q resolves to an empty or unset variable", ref)}
	}
	return value, nil
}

// isNotFound reports whether err indicates the command was not found on PATH.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "executable file not found")
}

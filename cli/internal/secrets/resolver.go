// Package secrets resolves secret *references* to their values at run time. A
// value is either a literal (returned unchanged) or a `<scheme>://<ref>`
// reference. Built-in schemes: `op://…` (1Password, via the `op` CLI),
// `env://VAR` (an environment variable), and `file://path` (a local file).
// Any other scheme is dispatched to a CLI-based password manager registered in
// portablevps.toml ([[secrets.manager]] with a scheme + command template), so
// Bitwarden, pass, gopass, etc. work without hardcoding each one.
//
// This lets the committed portablevps.toml and provider env files hold
// references instead of literal secrets: a human authenticates with their
// password manager, and CI passes literals via env:// — the same command works
// in both worlds.
package secrets

import (
	"fmt"
	"os"
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

// Manager is a CLI-based password manager registered for a reference scheme.
// Command is run with every "{ref}" token replaced by the part after the
// scheme; its stdout is the secret value.
type Manager struct {
	Scheme  string
	Command []string
}

// Resolver dereferences secret references.
type Resolver struct {
	Runner    Runner
	Getenv    func(string) string
	OpAccount string    // optional; passed to `op` as --account when set
	Managers  []Manager // extra managers registered in portablevps.toml
}

// IsReference reports whether s looks like a `<scheme>://<ref>` reference rather
// than a literal secret value.
func IsReference(s string) bool {
	_, _, ok := splitScheme(s)
	return ok
}

// Resolve returns the concrete value for a reference, or the input unchanged if
// it is a literal.
func (r Resolver) Resolve(value string) (string, error) {
	scheme, ref, ok := splitScheme(value)
	if !ok {
		return value, nil // literal
	}
	switch scheme {
	case "op":
		return r.resolveOp(value)
	case "env":
		return r.resolveEnv(value)
	case "file":
		return r.resolveFile(ref)
	default:
		return r.resolveManager(scheme, ref, value)
	}
}

// splitScheme splits "<scheme>://<ref>". The scheme must be a simple identifier;
// anything else (or no "://") is treated as a literal.
func splitScheme(value string) (scheme, ref string, ok bool) {
	i := strings.Index(value, "://")
	if i <= 0 {
		return "", "", false
	}
	scheme = value[:i]
	for _, c := range scheme {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", "", false
		}
	}
	return scheme, value[i+3:], true
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

func (r Resolver) resolveFile(path string) (string, error) {
	if path == "" {
		return "", &Error{Code: exitUsage, Msg: "file:// reference is missing a path"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{Code: exitAPIFailure, Msg: fmt.Sprintf("reading file:// reference %q: %v", path, err)}
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// resolveManager dispatches a reference to a configured CLI-based password
// manager, substituting {ref} into its command template.
func (r Resolver) resolveManager(scheme, ref, full string) (string, error) {
	for _, m := range r.Managers {
		if m.Scheme != scheme {
			continue
		}
		if len(m.Command) == 0 {
			return "", &Error{Code: exitUsage, Msg: fmt.Sprintf("secret manager %q has no command configured", scheme)}
		}
		args := make([]string, len(m.Command))
		for i, part := range m.Command {
			args[i] = strings.ReplaceAll(part, "{ref}", ref)
		}
		out, err := r.Runner.Run("", args[0], args[1:]...)
		if err != nil {
			if isNotFound(err) {
				return "", &Error{Code: exitUnavailable, Msg: fmt.Sprintf("%q (for scheme %s://) was not found on PATH to resolve %q", args[0], scheme, full)}
			}
			return "", &Error{Code: exitAPIFailure, Msg: fmt.Sprintf("resolving %q via the %s manager failed: %v", full, scheme, err)}
		}
		return strings.TrimRight(out, "\n"), nil
	}
	return "", &Error{Code: exitUsage, Msg: fmt.Sprintf("unknown secret-reference scheme %q in %q (register it under [[secrets.manager]] in portablevps.toml)", scheme, full)}
}

// isNotFound reports whether err indicates the command was not found on PATH.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "executable file not found")
}

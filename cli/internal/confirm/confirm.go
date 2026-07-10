// Package confirm implements the two-tier destructive-action gate: at a
// terminal the operator must type the resource name; in CI (non-interactive)
// the same guarantee is provided by an explicit --confirm <name> flag. The
// semantics are identical — only the input source differs. A plain --yes never
// satisfies a destructive gate.
package confirm

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ExitUsage is returned (as the code on Error) when confirmation is missing or
// does not match — a usage error, matching the CLI's sysexits convention.
const ExitUsage = 64

// Error is a confirmation failure carrying the CLI exit code.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// ExitCode reports the process exit code for a confirmation failure.
func (e *Error) ExitCode() int { return ExitUsage }

// Options controls how a destructive gate is satisfied.
type Options struct {
	Interactive bool      // prompt the operator (a real TTY)
	Confirm     string    // the --confirm value (CI channel)
	In          io.Reader // prompt input (interactive)
	Out         io.Writer // prompt output (interactive)
}

// Destroy gates a destructive operation on resource (e.g. a host IP or server
// name); kind names what the resource is ("host", "server") for the prompt.
// Returns nil to proceed, or an *Error to abort.
func Destroy(resource, kind string, o Options) error {
	if resource == "" {
		return &Error{Msg: "internal error: destructive gate called with an empty resource name"}
	}

	if !o.Interactive {
		if o.Confirm == "" {
			return &Error{Msg: fmt.Sprintf(
				"this operation destroys %s %q; pass --confirm %s to proceed non-interactively",
				kind, resource, resource)}
		}
		if o.Confirm != resource {
			return &Error{Msg: fmt.Sprintf(
				"--confirm %q does not match the %s to destroy (%q)", o.Confirm, kind, resource)}
		}
		return nil
	}

	fmt.Fprintf(o.Out, "This will DESTROY %s %q.\nType the %s to continue: ", kind, resource, kind)
	reader := bufio.NewReader(o.In)
	line, _ := reader.ReadString('\n')
	if strings.TrimSpace(line) != resource {
		return &Error{Msg: "confirmation did not match; aborting"}
	}
	return nil
}

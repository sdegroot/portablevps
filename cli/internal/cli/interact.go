package cli

import (
	"io"
	"os"

	"github.com/epistola-app/portablevps/internal/confirm"
)

// interactive reports whether the CLI should prompt the operator. Precedence:
// an explicit --interactive/--non-interactive flag wins; then --json (structured
// output implies non-interactive); then $CI; then whether stdin+stdout are a
// real terminal.
func (g *globalOptions) interactive() bool {
	switch {
	case g.interactiveFlag:
		return true
	case g.nonInteractive:
		return false
	case g.json:
		return false
	case os.Getenv("CI") != "":
		return false
	default:
		return isTerminal(os.Stdin) && isTerminal(os.Stdout)
	}
}

// confirmDestroy gates a destructive operation, using an interactive
// type-the-name prompt at a terminal or the --confirm flag otherwise.
func (g *globalOptions) confirmDestroy(resource, kind string, in io.Reader, out io.Writer) error {
	return confirm.Destroy(resource, kind, confirm.Options{
		Interactive: g.interactive(),
		Confirm:     g.confirm,
		In:          in,
		Out:         out,
	})
}

// isTerminal reports whether f is a character device (a TTY), without pulling in
// an extra dependency.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

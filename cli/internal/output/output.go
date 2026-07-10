// Package output renders results for humans or machines. Every command supports
// a --json mode so the CLI is scriptable and CI-friendly.
package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/epistola-app/portablevps/internal/core"
)

var statusLabel = map[core.Status]string{
	core.StatusOK:   "OK  ",
	core.StatusWarn: "WARN",
	core.StatusFail: "FAIL",
}

// DoctorReport is the machine-readable shape of a doctor run.
type DoctorReport struct {
	Checks   []core.Check `json:"checks"`
	Failures int          `json:"failures"`
	Warnings int          `json:"warnings"`
}

// RenderDoctorHuman writes a readable report.
func RenderDoctorHuman(w io.Writer, checks []core.Check) {
	for _, c := range checks {
		fmt.Fprintf(w, "[%s] %s\n", statusLabel[c.Status], c.Title)
		if c.Hint != "" {
			fmt.Fprintf(w, "         -> %s\n", c.Hint)
		}
	}
	fails, warns := core.Counts(checks)
	fmt.Fprintf(w, "\n%d checks: %d failed, %d warnings\n", len(checks), fails, warns)
}

// RenderDoctorJSON writes the machine-readable report.
func RenderDoctorJSON(w io.Writer, checks []core.Check) error {
	fails, warns := core.Counts(checks)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(DoctorReport{Checks: checks, Failures: fails, Warnings: warns})
}

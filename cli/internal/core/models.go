// Package core holds portablevps domain logic. It performs no argument parsing
// and owns no process globals: everything it needs (command execution, the
// environment, the consumer repo root) is passed in, so it is straightforward
// to unit-test and safe to drive from CI.
package core

// CommandRunner runs an external command in a working directory and returns its
// stdout. The CLI wires in a real exec-based implementation; tests inject a fake.
type CommandRunner interface {
	Run(dir, name string, args ...string) (stdout string, err error)
}

// Server is the subset of a consumer's logical server definition the CLI needs.
type Server struct {
	Name             string
	Provider         string
	BackupRepository string
	Hostname         string
	NetbirdName      string
	NetbirdGroups    []string
}

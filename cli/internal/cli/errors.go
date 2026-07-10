package cli

// ExitError lets a command choose the process exit code while flowing through
// cobra's normal error return. Any other error exits 1.
type ExitError struct {
	Code    int
	Message string
}

func (e ExitError) Error() string { return e.Message }

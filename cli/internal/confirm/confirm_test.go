package confirm

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNonInteractiveRequiresConfirmFlag(t *testing.T) {
	err := Destroy("1.2.3.4", "host", Options{Interactive: false})
	var e *Error
	if !errors.As(err, &e) || e.ExitCode() != ExitUsage {
		t.Fatalf("expected usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "--confirm 1.2.3.4") {
		t.Errorf("message should name the flag to pass: %q", err.Error())
	}
}

func TestNonInteractiveMismatchFails(t *testing.T) {
	err := Destroy("1.2.3.4", "host", Options{Interactive: false, Confirm: "9.9.9.9"})
	if err == nil {
		t.Fatal("expected mismatch to fail")
	}
}

func TestNonInteractiveMatchProceeds(t *testing.T) {
	if err := Destroy("1.2.3.4", "host", Options{Interactive: false, Confirm: "1.2.3.4"}); err != nil {
		t.Fatalf("matching confirm should proceed: %v", err)
	}
}

func TestInteractiveTypedMatchProceeds(t *testing.T) {
	var out bytes.Buffer
	err := Destroy("web-1", "server", Options{
		Interactive: true,
		In:          strings.NewReader("web-1\n"),
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("typed match should proceed: %v", err)
	}
	if !strings.Contains(out.String(), "DESTROY server \"web-1\"") {
		t.Errorf("prompt should name the resource: %q", out.String())
	}
}

func TestInteractiveTypedMismatchAborts(t *testing.T) {
	var out bytes.Buffer
	err := Destroy("web-1", "server", Options{
		Interactive: true,
		In:          strings.NewReader("wrong\n"),
		Out:         &out,
	})
	if err == nil {
		t.Fatal("typed mismatch should abort")
	}
}

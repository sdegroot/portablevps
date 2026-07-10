package secrets

import (
	"errors"
	"testing"
)

type fakeRunner struct {
	out  string
	err  error
	last []string
}

func (f *fakeRunner) Run(_, name string, args ...string) (string, error) {
	f.last = append([]string{name}, args...)
	return f.out, f.err
}

func TestLiteralPassesThrough(t *testing.T) {
	r := Resolver{Runner: &fakeRunner{}}
	got, err := r.Resolve("plain-value")
	if err != nil || got != "plain-value" {
		t.Fatalf("literal: got %q, %v", got, err)
	}
	if IsReference("plain-value") {
		t.Errorf("literal should not be a reference")
	}
}

func TestEnvReference(t *testing.T) {
	env := map[string]string{"TOKEN": "s3cr3t"}
	r := Resolver{Runner: &fakeRunner{}, Getenv: func(k string) string { return env[k] }}
	got, err := r.Resolve("env://TOKEN")
	if err != nil || got != "s3cr3t" {
		t.Fatalf("env ref: got %q, %v", got, err)
	}
}

func TestEnvReferenceUnsetIsUsageError(t *testing.T) {
	r := Resolver{Runner: &fakeRunner{}, Getenv: func(string) string { return "" }}
	_, err := r.Resolve("env://MISSING")
	var e *Error
	if !errors.As(err, &e) || e.Code != exitUsage {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestOpReferenceRunsOpRead(t *testing.T) {
	fr := &fakeRunner{out: "op-secret\n"}
	r := Resolver{Runner: fr, OpAccount: "my.1password.com"}
	got, err := r.Resolve("op://Vault/Item/field")
	if err != nil || got != "op-secret" {
		t.Fatalf("op ref: got %q, %v", got, err)
	}
	// account threaded through, trailing newline trimmed
	want := []string{"op", "read", "--account", "my.1password.com", "op://Vault/Item/field"}
	if len(fr.last) != len(want) {
		t.Fatalf("op args = %v, want %v", fr.last, want)
	}
	for i := range want {
		if fr.last[i] != want[i] {
			t.Fatalf("op args = %v, want %v", fr.last, want)
		}
	}
}

func TestOpMissingBinaryIsUnavailable(t *testing.T) {
	fr := &fakeRunner{err: errors.New("exec: \"op\": executable file not found in $PATH")}
	r := Resolver{Runner: fr}
	_, err := r.Resolve("op://Vault/Item/field")
	var e *Error
	if !errors.As(err, &e) || e.Code != exitUnavailable {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestResolveMapping(t *testing.T) {
	env := map[string]string{"A": "one"}
	r := Resolver{Runner: &fakeRunner{}, Getenv: func(k string) string { return env[k] }}
	out, err := r.ResolveMapping(map[string]string{"a": "env://A", "b": "literal"})
	if err != nil {
		t.Fatal(err)
	}
	if out["a"] != "one" || out["b"] != "literal" {
		t.Fatalf("mapping = %v", out)
	}
}

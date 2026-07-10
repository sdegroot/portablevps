package keystore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeOp struct {
	reads map[string]string // ref -> value
	err   error
	calls []string
}

func (f *fakeOp) Run(_ , name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.err != nil {
		return "", f.err
	}
	ref := args[len(args)-1]
	if v, ok := f.reads[ref]; ok {
		return v, nil
	}
	return "", os.ErrNotExist
}

func TestAgeMaterialFromOnePassword(t *testing.T) {
	op := &fakeOp{reads: map[string]string{"op://Epistola/web/age-key": "AGE-SECRET-KEY-1XYZ\n"}}
	s := Store{Runner: op, OpAccount: "acme.1password.com"}
	got, err := s.AgeMaterial(Ref{OpItem: "op://Epistola/web"})
	if err != nil || got != "AGE-SECRET-KEY-1XYZ" {
		t.Fatalf("age from op: got %q, %v", got, err)
	}
	if !strings.Contains(op.calls[0], "--account acme.1password.com") {
		t.Errorf("op account not threaded: %v", op.calls)
	}
}

func TestAgeMaterialFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "age-key.txt")
	_ = os.WriteFile(keyFile, []byte("AGE-SECRET-KEY-1FILE\n"), 0o600)

	// op fails; fallback file present
	op := &fakeOp{err: os.ErrNotExist}
	s := Store{Runner: op}
	got, err := s.AgeMaterial(Ref{OpItem: "op://Epistola/web", FilePath: keyFile})
	if err != nil || got != "AGE-SECRET-KEY-1FILE" {
		t.Fatalf("age fallback: got %q, %v", got, err)
	}
}

func TestSSHIdentityInteractiveUsesAgent(t *testing.T) {
	s := Store{AgentSock: "/tmp/agent.sock"}
	opts, cleanup, err := s.SSHIdentity(Ref{OpItem: "op://Epistola/web", PubPath: "/keys/web.pub"}, Interactive)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(opts, " ")
	if !strings.Contains(joined, "IdentityAgent=/tmp/agent.sock") || !strings.Contains(joined, "-i /keys/web.pub") {
		t.Fatalf("expected agent + pubkey selection, got %v", opts)
	}
}

func TestSSHIdentityHeadlessOpReadsToTempFile(t *testing.T) {
	op := &fakeOp{reads: map[string]string{"op://Epistola/web/private key": "PRIVATE-KEY-BODY"}}
	s := Store{Runner: op}
	opts, cleanup, err := s.SSHIdentity(Ref{OpItem: "op://Epistola/web"}, Headless)
	if err != nil {
		t.Fatal(err)
	}
	// -i <tempfile>
	var tmp string
	for i, o := range opts {
		if o == "-i" {
			tmp = opts[i+1]
		}
	}
	if tmp == "" {
		t.Fatalf("expected a temp key file, got %v", opts)
	}
	data, _ := os.ReadFile(tmp)
	if !strings.Contains(string(data), "PRIVATE-KEY-BODY") {
		t.Errorf("temp key content wrong: %q", data)
	}
	cleanup()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("cleanup should have removed the temp key")
	}
}

func TestSSHIdentityFileFallback(t *testing.T) {
	s := Store{}
	opts, _, err := s.SSHIdentity(Ref{FilePath: "/keys/web_ed25519"}, Interactive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts, " ") != "-i /keys/web_ed25519 -o IdentitiesOnly=yes" {
		t.Fatalf("file fallback opts wrong: %v", opts)
	}
}

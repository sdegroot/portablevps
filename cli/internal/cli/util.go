package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func fileExistsCli(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// toJSONString JSON-encodes a value the way `sops set` expects (mirrors the
// former Taskfile `jq -R .`).
func toJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// ensureEncryptedFile creates an empty sops-encrypted secrets file (matching the
// .sops.yaml creation_rule) if it does not exist yet, so `sops set` has a file to
// write into for a brand-new server. The transient plaintext is an empty map.
func ensureEncryptedFile(repoRoot, ageKey, rel string) error {
	full := filepath.Join(repoRoot, rel)
	if fileExistsCli(full) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte("{}\n"), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("sops", "--encrypt", "--in-place", rel)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "SOPS_AGE_KEY_FILE="+ageKey)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(full)
		return ExitError{Code: 70, Message: fmt.Sprintf("creating encrypted %s: %v: %s", rel, err, strings.TrimSpace(string(out)))}
	}
	return nil
}

// warnIfDirty prints the AGENTS.md gotcha: Nix flakes only see git-tracked
// files, so uncommitted new files (secrets, keys) can look "missing".
func warnIfDirty(w io.Writer, repoRoot string) {
	out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if err != nil {
		return
	}
	if strings.TrimSpace(string(out)) != "" {
		fmt.Fprintln(w, "warning: working tree has uncommitted changes — `git add` new files or the flake won't see them")
	}
}

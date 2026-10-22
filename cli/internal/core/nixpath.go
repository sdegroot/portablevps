package core

import (
	"os"
	"path/filepath"
	"strings"
)

// NixAddedToPath is the directory EnsureNixOnPath prepended to this process's
// PATH ("" when nix was already on PATH or nowhere to be found). The doctor
// reports it so the operator can fix their shell.
var NixAddedToPath string

// nixFallbackDirs are where the Nix installers put nix when the shell running
// pvps did not source their profile script (a non-login shell, a mise task, an
// IDE or agent terminal): the multi-user daemon profile, a single-user profile,
// and NixOS's system profile.
func nixFallbackDirs(getenv func(string) string) []string {
	dirs := []string{"/nix/var/nix/profiles/default/bin"}
	if home := getenv("HOME"); home != "" {
		dirs = append(dirs, filepath.Join(home, ".nix-profile", "bin"))
	}
	return append(dirs, "/run/current-system/sw/bin")
}

// EnsureNixOnPath makes `nix` resolvable when it is installed but not on PATH,
// by prepending the first fallback directory that holds an executable nix. It
// changes this process's PATH rather than resolving one absolute path because
// the children need it too: nixos-rebuild, run through `nix run`, calls `nix`
// itself. Returns the directory it added, or "".
func EnsureNixOnPath(lookPath func(string) (string, error), getenv func(string) string,
	setenv func(key, value string) error, isExecutable func(string) bool) string {
	if _, err := lookPath("nix"); err == nil {
		return ""
	}
	for _, dir := range nixFallbackDirs(getenv) {
		if !isExecutable(filepath.Join(dir, "nix")) {
			continue
		}
		path := dir
		if cur := getenv("PATH"); cur != "" {
			path = strings.Join([]string{dir, cur}, string(os.PathListSeparator))
		}
		if setenv("PATH", path) != nil {
			return ""
		}
		return dir
	}
	return ""
}

// IsExecutableFile reports whether path is a regular file with an execute bit.
func IsExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

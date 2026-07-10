package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pathInputRe = regexp.MustCompile(`path:\.\./([A-Za-z0-9._-]+)`)

// findPathInputs returns the sibling directory names a flake references as
// `path:../<name>` inputs (the monorepo pattern).
func findPathInputs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range pathInputRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// copyTree recursively copies src to dst, skipping any entry whose base name is
// in skip (e.g. .git, .local, result).
func copyTree(src, dst string, skip map[string]bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip[d.Name()] && path != src {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// stageFlake makes a consumer flake self-contained for remote evaluation: if it
// references siblings as `path:../<name>` (the monorepo layout), copy the flake
// and each referenced sibling into a temp tree, rewrite the inputs to in-tree
// paths, and re-lock. A standalone consumer is returned unchanged.
func stageFlake(repoRoot string, runner EnvRunner) (string, func(), error) {
	noop := func() {}
	flakeText, err := os.ReadFile(filepath.Join(repoRoot, "flake.nix"))
	if err != nil {
		return repoRoot, noop, nil
	}
	names := findPathInputs(string(flakeText))
	if len(names) == 0 {
		return repoRoot, noop, nil
	}
	tmp, err := os.MkdirTemp("", "portablevps-flake-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	skip := map[string]bool{".git": true, ".local": true, "result": true}
	if err := copyTree(repoRoot, tmp, skip); err != nil {
		cleanup()
		return "", noop, err
	}
	text := string(flakeText)
	for _, name := range names {
		src := filepath.Join(repoRoot, "..", name)
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			continue
		}
		if err := copyTree(src, filepath.Join(tmp, ".vendor", name), skip); err != nil {
			cleanup()
			return "", noop, err
		}
		text = strings.ReplaceAll(text, "path:../"+name, "path:./.vendor/"+name)
	}
	if err := os.WriteFile(filepath.Join(tmp, "flake.nix"), []byte(text), 0o644); err != nil {
		cleanup()
		return "", noop, err
	}
	_ = os.Remove(filepath.Join(tmp, "flake.lock"))
	if _, err := runner.Run(tmp, "nix", "--extra-experimental-features", "nix-command flakes", "flake", "lock", tmp); err != nil {
		cleanup()
		return "", noop, provisionErr(70, "re-locking vendored flake: %v", err)
	}
	return tmp, cleanup, nil
}

// ProvisionError carries a sysexits-style exit code.
type ProvisionError struct {
	Code int
	Msg  string
}

func (e *ProvisionError) Error() string { return e.Msg }
func (e *ProvisionError) ExitCode() int { return e.Code }
func provisionErr(code int, format string, a ...any) *ProvisionError {
	return &ProvisionError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// InstallEnv is the injected environment for provisioning a host.
type InstallEnv struct {
	FlakeDir string       // the consumer flake directory
	Stream   StreamRunner // nixos-anywhere, streamed live
	Runner   EnvRunner    // for `nix flake lock` when vendoring
	Report   func(phase, status, msg string)
}

// InstallOpts are the per-run install parameters.
type InstallOpts struct {
	Server         string   // logical server / nixosConfiguration name
	Target         string   // e.g. root@1.2.3.4
	SSHPort        string   // default "22"
	AgeKeyMaterial string   // host age key content, shipped to /etc/sops/age/keys.txt
	IdentityArgs   []string // nixos-anywhere identity args (e.g. ["-i","key"] or ["--ssh-option","IdentityAgent=…"])
	BuildOnRemote  bool     // build the closure on the target (cross-arch)
	RestoreMode    bool     // install the <server>-restore profile
}

// nixosAnywhereArgs builds the `nix run nixos-anywhere` argument vector. Pure, so
// it is unit-tested.
func nixosAnywhereArgs(flakeDir, profile, sshPort, extraFiles string, identity []string, buildOnRemote bool, target string) []string {
	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"run", "github:nix-community/nixos-anywhere", "--",
		"--flake", flakeDir + "#" + profile,
		"--ssh-port", sshPort,
		"--ssh-option", "StrictHostKeyChecking=no",
		"--ssh-option", "UserKnownHostsFile=/dev/null",
		"--extra-files", extraFiles,
	}
	args = append(args, identity...)
	if buildOnRemote {
		args = append(args, "--build-on-remote")
	}
	return append(args, target)
}

// Install provisions NixOS onto Target with nixos-anywhere: it stages the host
// age key into an extra-files tree (shipped to /etc/sops/age/keys.txt so sops-nix
// can decrypt the host's secrets), then runs nixos-anywhere against the flake,
// built on the remote for cross-arch operators.
func Install(env InstallEnv, opts InstallOpts) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	if opts.Target == "" {
		return provisionErr(64, "install target is required")
	}
	if opts.AgeKeyMaterial == "" {
		return provisionErr(66, "no host age key resolved for %s (1Password/file)", opts.Server)
	}
	port := opts.SSHPort
	if port == "" {
		port = "22"
	}

	tmp, err := os.MkdirTemp("", "portablevps-install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	ageDir := filepath.Join(tmp, "extra-files", "etc", "sops", "age")
	if err := os.MkdirAll(ageDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ageDir, "keys.txt"),
		[]byte(strings.TrimRight(opts.AgeKeyMaterial, "\n")+"\n"), 0o400); err != nil {
		return err
	}

	// Make the flake self-contained for the remote build (monorepo path inputs).
	flakeDir := env.FlakeDir
	if env.Runner != nil {
		report("stage", "run", "staging the flake for remote evaluation")
		staged, cleanup, err := stageFlake(env.FlakeDir, env.Runner)
		if err != nil {
			return err
		}
		defer cleanup()
		flakeDir = staged
	}

	profile := opts.Server
	if opts.RestoreMode {
		profile = opts.Server + "-restore"
	}
	args := nixosAnywhereArgs(flakeDir, profile, port,
		filepath.Join(tmp, "extra-files"), opts.IdentityArgs, opts.BuildOnRemote, opts.Target)

	report("install", "run", fmt.Sprintf("nixos-anywhere .#%s -> %s (built on the remote)", profile, opts.Target))
	if err := env.Stream.Stream(env.FlakeDir, nil, "nix", args...); err != nil {
		report("install", "fail", err.Error())
		return provisionErr(70, "nixos-anywhere failed: %v", err)
	}
	report("install", "ok", opts.Target+" installed .#"+profile)
	return nil
}

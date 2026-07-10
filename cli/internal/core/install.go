package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

	profile := opts.Server
	if opts.RestoreMode {
		profile = opts.Server + "-restore"
	}
	args := nixosAnywhereArgs(env.FlakeDir, profile, port,
		filepath.Join(tmp, "extra-files"), opts.IdentityArgs, opts.BuildOnRemote, opts.Target)

	report("install", "run", fmt.Sprintf("nixos-anywhere .#%s -> %s (built on the remote)", profile, opts.Target))
	if err := env.Stream.Stream(env.FlakeDir, nil, "nix", args...); err != nil {
		report("install", "fail", err.Error())
		return provisionErr(70, "nixos-anywhere failed: %v", err)
	}
	report("install", "ok", opts.Target+" installed .#"+profile)
	return nil
}

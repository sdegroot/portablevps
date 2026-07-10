package core

// ServiceEnv drives service data-movement (migrate/restore) over admin SSH. The
// HostRunner is host-parameterised, so one adapter reaches both source and
// target hosts.
type ServiceEnv struct {
	RepoRoot string
	Host     HostRunner
	Stream   StreamRunner
	Report   func(phase, status, msg string)
}

func (env ServiceEnv) report() func(string, string, string) {
	if env.Report == nil {
		return func(string, string, string) {}
	}
	return env.Report
}

// switchTo runs an in-place nixos-rebuild switch to profile on host, built on
// the remote.
func (env ServiceEnv) switchTo(profile, host string) error {
	return env.Stream.Stream(env.RepoRoot,
		map[string]string{"NIX_SSHOPTS": env.Host.NixSSHOpts()},
		"nix", "--extra-experimental-features", "nix-command flakes",
		"run", "nixpkgs#nixos-rebuild", "--", "switch",
		"--flake", env.RepoRoot+"#"+profile,
		"--target-host", "admin@"+host,
		"--build-host", "admin@"+host,
		"--elevate=sudo", "--no-reexec")
}

// MigrateOpts parameterises a service move between two running hosts.
type MigrateOpts struct {
	Server     string // target server config (the service's config)
	SourceHost string
	TargetHost string
	Marker     string // optional demo marker to verify before/after
}

// Migrate moves a service from SourceHost to TargetHost via its backup
// repository: put the target in restore mode, take a final source backup, stop
// the source, restore onto the target, switch the target to normal, and verify.
// DNS repointing is left to `network dns-sync` (printed as the final step).
func Migrate(env ServiceEnv, o MigrateOpts) error {
	report := env.report()

	report("prepare-target", "run", "switching "+o.TargetHost+" to restore mode")
	if err := env.switchTo(o.Server+"-restore", o.TargetHost); err != nil {
		return provisionErr(70, "switching target to restore mode: %v", err)
	}
	if _, err := env.Host.Run(o.TargetHost, `test "$(cat /etc/portablevps/restore-mode)" = true`); err != nil {
		return provisionErr(71, "target %s is not in restore mode: %v", o.TargetHost, err)
	}

	if o.Marker != "" {
		if _, err := env.Host.Run(o.SourceHost, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return provisionErr(71, "source marker verification failed: %v", err)
		}
	}

	report("backup", "run", "final source backup on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl start portablevps-backup.service"); err != nil {
		return provisionErr(70, "final source backup failed: %v", err)
	}

	report("cutover", "run", "stopping app services on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl stop apps.target || true"); err != nil {
		return provisionErr(70, "stopping source apps: %v", err)
	}

	report("restore", "run", "restoring the backup onto "+o.TargetHost)
	if _, err := env.Host.Run(o.TargetHost, "sudo restore.sh"); err != nil {
		return provisionErr(70, "restore on target failed: %v", err)
	}

	report("finalize", "run", "switching "+o.TargetHost+" to normal and starting apps")
	if err := env.switchTo(o.Server, o.TargetHost); err != nil {
		return provisionErr(70, "switching target to normal: %v", err)
	}
	if _, err := env.Host.Run(o.TargetHost, "sudo systemctl start apps.target"); err != nil {
		return provisionErr(70, "starting apps on target: %v", err)
	}
	if o.Marker != "" {
		if _, err := env.Host.Run(o.TargetHost, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return provisionErr(71, "target marker verification failed: %v", err)
		}
	}
	report("done", "ok", "migrated to "+o.TargetHost+" — run `network dns-sync` to repoint DNS")
	return nil
}

// RestoreOpts parameterises restoring a service onto an already-installed host.
type RestoreOpts struct {
	Server string
	Host   string
	Marker string
}

// Restore brings a service up on an already-installed host from its backup
// repository: put the host in restore mode, run restore.sh, switch to normal,
// start apps, and verify. Used for disaster recovery onto a fresh/replacement box.
func Restore(env ServiceEnv, o RestoreOpts) error {
	report := env.report()

	report("prepare", "run", "switching "+o.Host+" to restore mode")
	if err := env.switchTo(o.Server+"-restore", o.Host); err != nil {
		return provisionErr(70, "switching to restore mode: %v", err)
	}
	if _, err := env.Host.Run(o.Host, `test "$(cat /etc/portablevps/restore-mode)" = true`); err != nil {
		return provisionErr(71, "%s is not in restore mode: %v", o.Host, err)
	}

	report("restore", "run", "restoring the backup onto "+o.Host)
	if _, err := env.Host.Run(o.Host, "sudo restore.sh"); err != nil {
		return provisionErr(70, "restore failed: %v", err)
	}

	report("finalize", "run", "switching "+o.Host+" to normal and starting apps")
	if err := env.switchTo(o.Server, o.Host); err != nil {
		return provisionErr(70, "switching to normal: %v", err)
	}
	if _, err := env.Host.Run(o.Host, "sudo systemctl start apps.target"); err != nil {
		return provisionErr(70, "starting apps: %v", err)
	}
	if o.Marker != "" {
		if _, err := env.Host.Run(o.Host, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return provisionErr(71, "marker verification failed: %v", err)
		}
	}
	report("done", "ok", o.Host+" restored .#"+o.Server)
	return nil
}

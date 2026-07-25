package core

import "strings"

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
	nixSSHOpts := env.Host.NixSSHOpts()
	if routed, ok := env.Host.(interface{ NixSSHOptsFor(string) string }); ok {
		nixSSHOpts = routed.NixSSHOptsFor(host)
	}
	if err := env.Stream.Stream(env.RepoRoot,
		map[string]string{"NIX_SSHOPTS": nixSSHOpts},
		"nix", "--extra-experimental-features", "nix-command flakes",
		"run", NixpkgsFlake+"#nixos-rebuild", "--", "switch",
		"--flake", env.RepoRoot+"#"+profile,
		"--target-host", "admin@"+host,
		"--build-host", "admin@"+host,
		"--elevate=sudo", "--no-reexec"); err != nil {
		return err
	}
	if switched, ok := env.Host.(interface{ SwitchedTo(profile, host string) }); ok {
		switched.SwitchedTo(profile, host)
	}
	return nil
}

// MigrateOpts parameterises a service move between two running hosts.
type MigrateOpts struct {
	Server     string // target server config (the service's config)
	SourceHost string
	TargetHost string
	Marker     string // optional demo marker to verify before/after
}

// Migrate moves a service from SourceHost to TargetHost via its backup
// repository: put the target in restore mode, stop the source, take a final
// source backup, restore onto the target, switch the target to normal, and
// verify. The source is stopped BEFORE the final backup so no writes land
// between the snapshot and the cutover (backing up a still-live source would
// lose any write made in that window). DNS repointing is left to
// `network dns-sync` (printed as the final step).
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

	// Quiesce the source first so the final backup is a consistent snapshot with
	// no concurrent writes. A failed stop aborts the migration — proceeding would
	// back up (and cut over to) a still-live source and silently lose writes.
	report("cutover", "run", "stopping app services on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl stop apps.target"); err != nil {
		return provisionErr(70, "stopping source apps: %v", err)
	}

	report("backup", "run", "final source backup on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl start portablevps-backup.service"); err != nil {
		return provisionErr(70, "final source backup failed: %v", err)
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

// DrillOpts parameterises a real-infrastructure disaster-recovery drill.
type DrillOpts struct {
	Server      string
	SourceHost  string // live host to seed a marker on and back up (left otherwise untouched)
	RestoreHost string // host to restore onto and verify (destructive to its data)
}

// RestoreDrill proves backups actually restore, end to end, against real hosts:
// it seeds a unique verification marker into the source's live data, backs the
// source up, then restores that backup onto RestoreHost and verifies the marker
// survived the round trip. The source is only seeded (non-destructive); the
// restore host is rebuilt from the backup. It returns the marker it used.
func RestoreDrill(env ServiceEnv, o DrillOpts) (string, error) {
	report := env.report()

	report("seed", "run", "seeding a verification marker on "+o.SourceHost)
	out, err := env.Host.Run(o.SourceHost, "sudo insert-test-data.sh | tail -n 1")
	if err != nil {
		return "", provisionErr(70, "seeding marker on %s: %v", o.SourceHost, err)
	}
	marker := strings.TrimSpace(out)
	if marker == "" {
		return "", provisionErr(70, "insert-test-data.sh returned an empty marker on %s", o.SourceHost)
	}
	report("seed", "ok", "marker "+marker)

	report("backup", "run", "backing up "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo init-backup-repo.sh; sudo backup.sh"); err != nil {
		return marker, provisionErr(70, "backup on %s: %v", o.SourceHost, err)
	}

	// Restore onto the (separate) restore host and verify the marker survived.
	if err := Restore(env, RestoreOpts{Server: o.Server, Host: o.RestoreHost, Marker: marker}); err != nil {
		return marker, err
	}
	return marker, nil
}

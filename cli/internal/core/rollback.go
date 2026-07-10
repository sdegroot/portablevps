package core

import "fmt"

// RollbackEnv drives a NixOS generation rollback over admin SSH. No local build
// is involved — a previous generation already exists on the host.
type RollbackEnv struct {
	Host   HostRunner
	Report func(phase, status, msg string)
}

const systemProfile = "/nix/var/nix/profiles/system"

// ListGenerations returns the host's NixOS system generations.
func ListGenerations(env RollbackEnv, host string) (string, error) {
	out, err := env.Host.Run(host, "sudo nix-env --list-generations -p "+systemProfile)
	if err != nil {
		return "", deployErr(70, "listing generations on %s: %v", host, err)
	}
	return out, nil
}

// Rollback activates a previous NixOS generation on the host (config/version
// rollback, distinct from a data restore). toGen == 0 means the immediately
// previous generation; a positive toGen switches to that specific generation.
func Rollback(env RollbackEnv, host string, toGen int) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}

	var cmd, desc string
	if toGen > 0 {
		cmd = fmt.Sprintf("sudo nix-env --switch-generation %d -p %s && sudo %s/bin/switch-to-configuration switch",
			toGen, systemProfile, systemProfile)
		desc = fmt.Sprintf("switching to generation %d", toGen)
	} else {
		cmd = "sudo nixos-rebuild switch --rollback"
		desc = "rolling back to the previous generation"
	}

	report("rollback", "run", desc+" on "+host)
	out, err := env.Host.Run(host, cmd)
	if err != nil {
		report("rollback", "fail", err.Error())
		return deployErr(70, "rollback on %s: %v", host, err)
	}
	report("rollback", "ok", firstLine(out))
	return nil
}

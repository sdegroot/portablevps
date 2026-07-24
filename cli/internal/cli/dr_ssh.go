package cli

import (
	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
)

type routedSSH struct {
	defaultSSH adapters.SSH
	byHost     map[string]adapters.SSH
}

func (r routedSSH) sshFor(host string) adapters.SSH {
	if ssh, ok := r.byHost[host]; ok {
		return ssh
	}
	return r.defaultSSH
}

func (r routedSSH) Run(host, command string) (string, error) {
	return r.sshFor(host).Run(host, command)
}

func (r routedSSH) RunInput(host, command, input string) (string, error) {
	return r.sshFor(host).RunInput(host, command, input)
}

func (r routedSSH) WaitReady(host string) error {
	return r.sshFor(host).WaitReady(host)
}

func (r routedSSH) NixSSHOpts() string {
	return r.defaultSSH.NixSSHOpts()
}

func (r routedSSH) NixSSHOptsFor(host string) string {
	return r.sshFor(host).NixSSHOpts()
}

func (r routedSSH) SwitchedTo(_profile, host string) {
	delete(r.byHost, host)
}

func drHostRunner(g *globalOptions, ctx *config.Context, server string, flags drFlags, sourceHost, restoreHost string) (core.HostRunner, func(), error) {
	defaultSSH, cleanupDefault, err := sshForServer(g, ctx, server, flags.sshPort, flags.adminKey)
	if err != nil {
		return nil, func() {}, err
	}
	cleanups := []func(){cleanupDefault}
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// An explicit admin-key override is intentionally single-identity: the
	// operator is telling us which key reaches both remote hosts.
	if flags.adminKey != "" || (flags.sourceServer == "" && flags.restoreServer == "") {
		return defaultSSH, cleanup, nil
	}

	routed := routedSSH{
		defaultSSH: defaultSSH,
		byHost:     map[string]adapters.SSH{},
	}
	if flags.sourceServer != "" {
		ssh, c, err := sshForServer(g, ctx, flags.sourceServer, flags.sshPort, "")
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, c)
		routed.byHost[sourceHost] = ssh
	}
	if flags.restoreServer != "" {
		ssh, c, err := sshForServer(g, ctx, flags.restoreServer, flags.sshPort, "")
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, c)
		routed.byHost[restoreHost] = ssh
	}
	return routed, cleanup, nil
}

func sshForServer(g *globalOptions, ctx *config.Context, server, sshPort, adminKey string) (adapters.SSH, func(), error) {
	hf := hostFlags{sshPort: sshPort, adminKey: adminKey}
	return hf.sshAdapter(g, ctx, server)
}

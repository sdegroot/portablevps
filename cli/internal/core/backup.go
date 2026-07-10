package core

import "strings"

// BackupEnv drives the on-host backup subsystem over admin SSH.
type BackupEnv struct {
	Host   HostRunner
	Report func(phase, status, msg string)
}

// RunBackup triggers the host's backup service now and waits for it to finish
// (systemctl start blocks until the oneshot completes).
func RunBackup(env BackupEnv, host string) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	report("backup", "run", "starting portablevps-backup.service on "+host)
	if _, err := env.Host.Run(host, "sudo systemctl start portablevps-backup.service"); err != nil {
		report("backup", "fail", err.Error())
		return deployErr(70, "backup on %s failed: %v", host, err)
	}
	report("backup", "ok", "backup completed")
	return nil
}

// BackupStatus returns the host's backup timer/service state and last run. The
// remote command always exits 0 so an inactive service is reported, not an error.
func BackupStatus(env BackupEnv, host string) (string, error) {
	const cmd = "systemctl list-timers portablevps-backup.timer --no-pager 2>/dev/null; " +
		"echo '---'; " +
		"systemctl status portablevps-backup.service --no-pager -l 2>/dev/null | tail -n 12; " +
		"true"
	out, err := env.Host.Run(host, cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", deployErr(70, "backup status on %s: %v", host, err)
	}
	return out, nil
}

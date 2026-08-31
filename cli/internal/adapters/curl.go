package adapters

// CurlCertChecker probes a host for a served TLS certificate using curl's
// --connect-to to bypass DNS, without needing a real A/AAAA record for
// domain yet. Satisfies core.CertPrewarmer.
type CurlCertChecker struct{}

// Prime makes a single insecure request to trigger certificate
// issuance/loading on first contact (SNI does not yet need a valid chain).
func (CurlCertChecker) Prime(domain, host string) error {
	_, err := (ExecRunner{}).Run("", "curl",
		"--head", "--silent", "--show-error", "--insecure",
		"--connect-timeout", "10", "--max-time", "20",
		"--connect-to", domain+":443:"+host+":443",
		"https://"+domain+"/")
	return err
}

// Valid reports whether host currently serves a trusted certificate for
// domain. A failure here (transport, TLS, or HTTP) means "not ready yet",
// not a hard error — the caller retries.
func (CurlCertChecker) Valid(domain, host string) (bool, error) {
	_, err := (ExecRunner{}).Run("", "curl",
		"--head", "--silent", "--show-error",
		"--connect-timeout", "10", "--max-time", "20",
		"--connect-to", domain+":443:"+host+":443",
		"https://"+domain+"/")
	return err == nil, nil
}

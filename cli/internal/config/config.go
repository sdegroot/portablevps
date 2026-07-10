// Package config resolves operator inputs with a clear precedence:
//
//	flag > env > .local/portablevps.local.toml > portablevps.toml > built-in default
//
// The committed portablevps.toml holds repo defaults; the gitignored
// .local/portablevps.local.toml holds per-operator overrides. Secret-bearing
// fields hold *references* (op://, env://) resolved at run time, never literals,
// so the committed file is review-safe.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Flags are the resolved command-line values the CLI layer passes in. Fields are
// empty when the flag was not set, so lower-precedence sources can win.
type Flags struct {
	Project string
	Server  string
}

// Context is the fully-merged operator context every command runs against.
type Context struct {
	RepoRoot       string
	Server         string
	DNSZone        string
	PublicDNSZone  string
	OpAccount      string
	OperatorAgeKey string
	Vault          string // 1Password vault holding per-server key items (empty = file-only)
	Network        NetworkConfig
	Providers      map[string]ProviderConfig
}

// NetworkConfig holds mesh-backend settings (NetBird today).
type NetworkConfig struct {
	APIURL   string
	APIToken string
	DNSZone  string
}

// ProviderConfig holds per-provider settings.
type ProviderConfig struct {
	Token string
}

// file mirrors the on-disk portablevps.toml schema.
type file struct {
	DefaultServer string `toml:"default_server"`
	DNSZone       string `toml:"dns_zone"`
	PublicDNSZone string `toml:"public_dns_zone"`
	Vault         string `toml:"vault"`
	Secrets       struct {
		OpAccount      string `toml:"op_account"`
		OperatorAgeKey string `toml:"operator_age_key"`
	} `toml:"secrets"`
	Network struct {
		APIURL   string `toml:"api_url"`
		APIToken string `toml:"api_token"`
		DNSZone  string `toml:"dns_zone"`
	} `toml:"network"`
	Provider map[string]struct {
		Token string `toml:"token"`
	} `toml:"provider"`
}

// ResolveRepoRoot returns the absolute consumer repository path: the --project
// flag, else $PORTABLEVPS_PROJECT, else the current working directory.
func ResolveRepoRoot(flagProject string) (string, error) {
	root := flagProject
	if root == "" {
		root = os.Getenv("PORTABLEVPS_PROJECT")
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determining current directory: %w", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving project root %q: %w", root, err)
	}
	return abs, nil
}

// Resolve merges every configuration source into a Context using the documented
// precedence. getenv is injected for testability (pass os.Getenv in production).
func Resolve(flags Flags, getenv func(string) string) (*Context, error) {
	repoRoot, err := ResolveRepoRoot(flags.Project)
	if err != nil {
		return nil, err
	}

	repoFile, err := loadFile(filepath.Join(repoRoot, "portablevps.toml"))
	if err != nil {
		return nil, err
	}
	localFile, err := loadFile(filepath.Join(repoRoot, ".local", "portablevps.local.toml"))
	if err != nil {
		return nil, err
	}

	ctx := &Context{
		RepoRoot: repoRoot,
		Server: firstNonEmpty(
			flags.Server, getenv("SERVER"), getenv("DEPLOYMENT"),
			localFile.DefaultServer, repoFile.DefaultServer),
		DNSZone: firstNonEmpty(
			getenv("NETBIRD_DNS_ZONE"), localFile.Network.DNSZone, repoFile.Network.DNSZone,
			localFile.DNSZone, repoFile.DNSZone),
		PublicDNSZone:  firstNonEmpty(localFile.PublicDNSZone, repoFile.PublicDNSZone),
		OpAccount:      firstNonEmpty(getenv("OP_ACCOUNT"), localFile.Secrets.OpAccount, repoFile.Secrets.OpAccount),
		OperatorAgeKey: firstNonEmpty(getenv("SOPS_AGE_KEY_FILE"), localFile.Secrets.OperatorAgeKey, repoFile.Secrets.OperatorAgeKey, ".local/sops/age-key.txt"),
		Vault:          firstNonEmpty(getenv("PORTABLEVPS_VAULT"), localFile.Vault, repoFile.Vault),
		Network: NetworkConfig{
			APIURL:   firstNonEmpty(getenv("NETBIRD_API_URL"), localFile.Network.APIURL, repoFile.Network.APIURL),
			APIToken: firstNonEmpty(getenv("NETBIRD_API_TOKEN"), localFile.Network.APIToken, repoFile.Network.APIToken),
			DNSZone:  firstNonEmpty(getenv("NETBIRD_DNS_ZONE"), localFile.Network.DNSZone, repoFile.Network.DNSZone),
		},
		Providers: mergeProviders(repoFile, localFile),
	}
	return ctx, nil
}

func mergeProviders(repoFile, localFile file) map[string]ProviderConfig {
	out := map[string]ProviderConfig{}
	for name, p := range repoFile.Provider {
		out[name] = ProviderConfig{Token: p.Token}
	}
	for name, p := range localFile.Provider {
		merged := out[name]
		if p.Token != "" {
			merged.Token = p.Token
		}
		out[name] = merged
	}
	return out
}

// loadFile parses a portablevps.toml; a missing file yields a zero-value file
// (not an error), so config files are optional.
func loadFile(path string) (file, error) {
	var f file
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parsing %s: %w", path, err)
	}
	return f, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

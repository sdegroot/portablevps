package core

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// rawServer mirrors the shape of one entry in the consumer flake's `.#serverInfo`
// output (and the SERVER_REGISTRY test fixture).
type rawServer struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Placement struct {
		Provider string `json:"provider"`
	} `json:"placement"`
	BackupRepository string `json:"backupRepository"`
	Hostname         string `json:"hostname"`
	NetbirdName      string `json:"netbirdName"`
	Netbird          struct {
		Groups []string `json:"groups"`
	} `json:"netbird"`
}

func (r rawServer) toServer(name string) Server {
	provider := r.Placement.Provider
	if provider == "" {
		provider = r.Provider
	}
	hostname := r.Hostname
	if hostname == "" {
		hostname = name
	}
	netbird := r.NetbirdName
	if netbird == "" {
		netbird = hostname
	}
	return Server{
		Name:             name,
		Provider:         provider,
		BackupRepository: r.BackupRepository,
		Hostname:         hostname,
		NetbirdName:      netbird,
		NetbirdGroups:    r.Netbird.Groups,
	}
}

// LoadServers returns the consumer's logical servers. It reads SERVER_REGISTRY
// (a JSON file) when set — the CI/test path — and otherwise evaluates the
// consumer flake's `.#serverInfo` output via nix. Mirrors the Python CLI's
// load_servers so the two stay behaviourally aligned during the migration.
func LoadServers(env Env) (map[string]Server, error) {
	var data []byte
	if registry := env.Getenv("SERVER_REGISTRY"); registry != "" {
		var err error
		data, err = os.ReadFile(registry)
		if err != nil {
			return nil, fmt.Errorf("reading SERVER_REGISTRY %q: %w", registry, err)
		}
	} else {
		out, err := env.Runner.Run(env.RepoRoot,
			"nix", "--extra-experimental-features", "nix-command flakes",
			"eval", "--json", ".#serverInfo")
		if err != nil {
			return nil, fmt.Errorf("nix eval .#serverInfo: %w", err)
		}
		data = []byte(out)
	}

	raw := map[string]rawServer{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing server registry: %w", err)
	}
	servers := make(map[string]Server, len(raw))
	for name, r := range raw {
		servers[name] = r.toServer(name)
	}
	return servers, nil
}

// LoadFleetNetbird returns the raw JSON of the consumer flake's `.#netbird`
// output (fleet-level access policies + disableDefaultPolicy). It returns an
// empty object when the consumer declares none. The caller unmarshals it into
// the netbird package's FleetPolicies to avoid a core->netbird dependency.
func LoadFleetNetbird(env Env) ([]byte, error) {
	if override := env.Getenv("NETBIRD_CONFIG"); override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("reading NETBIRD_CONFIG %q: %w", override, err)
		}
		return data, nil
	}
	out, err := env.Runner.Run(env.RepoRoot,
		"nix", "--extra-experimental-features", "nix-command flakes",
		"eval", "--json", ".#netbird")
	if err != nil {
		// A consumer with no fleet netbird output is not an error.
		return []byte("{}"), nil
	}
	if out == "" {
		return []byte("{}"), nil
	}
	return []byte(out), nil
}

// SortedNames returns the server names in stable order (for deterministic output).
func SortedNames(servers map[string]Server) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

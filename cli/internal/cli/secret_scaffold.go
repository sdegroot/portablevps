package cli

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
)

// declaredSecrets returns the sops secret names the server's config declares
// (e.g. "netbird/setup-key"), so the editor can be seeded with exactly what the
// server needs.
func declaredSecrets(ctx *config.Context, server string) ([]string, error) {
	out, err := adapters.ExecRunner{}.Run(ctx.RepoRoot, "nix",
		"--extra-experimental-features", "nix-command flakes", "eval", "--json",
		".#nixosConfigurations."+server+".config.sops.secrets", "--apply", "builtins.attrNames")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		return nil, err
	}
	return names, nil
}

// presentSecretPaths returns the slash-delimited paths already present in the
// decrypted secrets file (best-effort; a missing/undecryptable file is empty).
func presentSecretPaths(ctx *config.Context, ageEnv map[string]string, rel string) map[string]bool {
	present := map[string]bool{}
	out, err := adapters.ExecRunner{}.RunEnv(ctx.RepoRoot, ageEnv, "sops", "-d", "--output-type", "yaml", rel)
	if err != nil {
		return present
	}
	var doc any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		return present
	}
	flattenPaths("", doc, present)
	return present
}

func flattenPaths(prefix string, v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			flattenPaths(join(prefix, k), val, out)
		}
	case map[any]any:
		for k, val := range t {
			flattenPaths(join(prefix, toStr(k)), val, out)
		}
	default:
		if prefix != "" {
			out[prefix] = true
		}
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// sopsIndex converts a secret name "a/b/c" to a sops key path '["a"]["b"]["c"]'.
func sopsIndex(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(name, "/") {
		b.WriteString(`["` + part + `"]`)
	}
	return b.String()
}

// scaffoldSecrets adds an empty placeholder for each declared secret that is not
// yet present in the file, so `secret edit` shows the operator exactly what to
// fill. Best-effort: it never overwrites an existing value and tolerates a flake
// that does not evaluate.
func scaffoldSecrets(ctx *config.Context, server string, ageEnv map[string]string, rel string) (int, error) {
	names, err := declaredSecrets(ctx, server)
	if err != nil || len(names) == 0 {
		return 0, nil
	}
	present := presentSecretPaths(ctx, ageEnv, rel)
	added := 0
	for _, name := range names {
		if present[name] {
			continue
		}
		if _, err := (adapters.ExecRunner{}).RunEnv(ctx.RepoRoot, ageEnv, "sops", "set", rel, sopsIndex(name), `""`); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// Package scaffold creates a new consumer repository from the server template
// embedded in the CLI, so `pvps init` works without Nix or network access.
//
// The template under template/ is the source of truth. The repository's
// templates/server (used by `nix flake init -t`) is that template rendered for
// a server named "example"; a test keeps the two identical.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:template
var templateFS embed.FS

// serverPlaceholder is the template file name replaced by the server name.
const serverPlaceholder = "SERVER"

// DefaultFlakeURL is the portablevps flake a consumer follows when no release
// version is known (development builds).
const DefaultFlakeURL = "github:sdegroot/portablevps"

// Data parameterises the template.
type Data struct {
	Server      string // logical server name; also servers/<Server>.nix
	FlakeURL    string // portablevps flake input URL
	PvpsVersion string // CLI version pinned in mise.toml
}

// Action is what Apply did with one file.
type Action string

const (
	Created Action = "created"
	Skipped Action = "skipped" // already present; left untouched
	Updated Action = "updated" // already present; required lines appended
)

// Result is one file's outcome.
type Result struct {
	Path   string `json:"path"`
	Action Action `json:"action"`
}

var serverNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateServerName checks the name is usable as a file name, NixOS host name
// and mesh peer name (an RFC 1123 label, lower case, starting with a letter).
func ValidateServerName(name string) error {
	if !serverNameRe.MatchString(name) {
		return fmt.Errorf("invalid server name %q: use lower-case letters, digits and hyphens, start with a letter, end with a letter or digit, at most 63 characters", name)
	}
	return nil
}

// DataFor derives the template data for a CLI version. A release version
// ("1.2.3") pins both the flake input and mise to that release; anything else
// (a development build) follows the default branch and mise's latest release.
func DataFor(server, version string) Data {
	d := Data{Server: server, FlakeURL: DefaultFlakeURL, PvpsVersion: "latest"}
	if isReleaseVersion(version) {
		d.FlakeURL = DefaultFlakeURL + "/v" + version
		d.PvpsVersion = version
	}
	return d
}

var releaseRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func isReleaseVersion(v string) bool { return releaseRe.MatchString(v) }

// Render returns every template file, keyed by its slash-separated path
// relative to the repository root, rendered with data.
func Render(data Data) (map[string][]byte, error) {
	if err := ValidateServerName(data.Server); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	err := fs.WalkDir(templateFS, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := templateFS.ReadFile(p)
		if err != nil {
			return err
		}
		tmpl, err := template.New(p).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", p, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("rendering template %s: %w", p, err)
		}
		rel := strings.TrimPrefix(p, "template/")
		if path.Base(rel) == serverPlaceholder+".nix" {
			rel = path.Join(path.Dir(rel), data.Server+".nix")
		}
		files[rel] = buf.Bytes()
		return nil
	})
	return files, err
}

// gitignoreRequired are lines an existing .gitignore must contain so private
// keys and lifecycle state under .local/ are never committed.
var gitignoreRequired = []string{".local/", "**/.local/"}

// Apply writes the rendered files under root. It never overwrites: an existing
// file is skipped, except that an existing .gitignore gets the .local/ rules
// appended when it lacks them. Results are sorted by path.
func Apply(root string, files map[string][]byte) ([]Result, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	results := make([]Result, 0, len(paths))
	for _, rel := range paths {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		existing, err := os.ReadFile(dst)
		switch {
		case err == nil && rel == ".gitignore":
			updated, changed := ensureLines(existing, gitignoreRequired)
			if !changed {
				results = append(results, Result{rel, Skipped})
				continue
			}
			if err := os.WriteFile(dst, updated, 0o644); err != nil {
				return results, err
			}
			results = append(results, Result{rel, Updated})
			continue
		case err == nil:
			results = append(results, Result{rel, Skipped})
			continue
		case !os.IsNotExist(err):
			return results, fmt.Errorf("checking %s: %w", dst, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return results, err
		}
		if err := os.WriteFile(dst, files[rel], 0o644); err != nil {
			return results, err
		}
		results = append(results, Result{rel, Created})
	}
	return results, nil
}

// ensureLines appends any of want missing from content (compared trimmed).
func ensureLines(content []byte, want []string) ([]byte, bool) {
	have := map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return content, false
	}
	out := string(content)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	out += "# portablevps: local secrets, generated private keys and lifecycle state.\n"
	out += strings.Join(missing, "\n") + "\n"
	return []byte(out), true
}

package scaffold

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRenderSubstitutesServerAndVersion(t *testing.T) {
	files, err := Render(DataFor("beachtrainer", "0.6.0"))
	if err != nil {
		t.Fatal(err)
	}
	server, ok := files["servers/beachtrainer.nix"]
	if !ok {
		t.Fatalf("servers/beachtrainer.nix not rendered; got %v", keys(files))
	}
	if _, ok := files["servers/SERVER.nix"]; ok {
		t.Fatal("placeholder file name leaked")
	}
	for _, want := range []string{
		`name = "beachtrainer";`,
		"../keys/servers/beachtrainer-admin.pub",
		"../secrets/beachtrainer.yaml",
	} {
		if !strings.Contains(string(server), want) {
			t.Errorf("server file lacks %q", want)
		}
	}
	if !strings.Contains(string(files["flake.nix"]), `url = "github:sdegroot/portablevps/v0.6.0";`) {
		t.Errorf("flake not pinned to the release:\n%s", files["flake.nix"])
	}
	if !strings.Contains(string(files["mise.toml"]), `"github:sdegroot/portablevps" = "0.6.0"`) {
		t.Errorf("mise.toml not pinned to the release:\n%s", files["mise.toml"])
	}
	if !strings.Contains(string(files["portablevps.toml"]), `default_server = "beachtrainer"`) {
		t.Errorf("portablevps.toml lacks default_server")
	}
	for name, content := range files {
		if strings.Contains(string(content), "{{") {
			t.Errorf("%s has an unrendered template action", name)
		}
	}
	for _, dotfile := range []string{".gitignore", ".sops.yaml", "keys/.gitkeep", "secrets/.gitkeep"} {
		if _, ok := files[dotfile]; !ok {
			t.Errorf("%s missing from the embedded template", dotfile)
		}
	}
}

func TestDataForDevBuildFollowsDefaultBranch(t *testing.T) {
	for _, v := range []string{"dev", "0.0.0-dev.abc123", "1.2.3-rc1", ""} {
		d := DataFor("x", v)
		if d.FlakeURL != DefaultFlakeURL || d.PvpsVersion != "latest" {
			t.Errorf("version %q: got %+v", v, d)
		}
	}
}

func TestValidateServerName(t *testing.T) {
	for _, ok := range []string{"a", "web", "beach-trainer", "app2", strings.Repeat("a", 63)} {
		if err := ValidateServerName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Web", "2app", "app-", "a_b", "a.b", "../x", strings.Repeat("a", 64)} {
		if err := ValidateServerName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestApplyNeverOverwritesAndPatchesGitignore(t *testing.T) {
	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	gitignore := filepath.Join(root, ".gitignore")
	mustWrite(t, readme, "# My app\n")
	mustWrite(t, gitignore, "node_modules\ndist") // no trailing newline

	files, err := Render(DataFor("web", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	results, err := Apply(root, files)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Action{}
	for _, r := range results {
		got[r.Path] = r.Action
	}
	if got["README.md"] != Skipped || got[".gitignore"] != Updated || got["servers/web.nix"] != Created {
		t.Fatalf("unexpected actions: %v", got)
	}
	if b, _ := os.ReadFile(readme); string(b) != "# My app\n" {
		t.Fatalf("README overwritten: %q", b)
	}
	b, _ := os.ReadFile(gitignore)
	if !strings.HasPrefix(string(b), "node_modules\ndist\n") || !strings.Contains(string(b), "\n.local/\n") {
		t.Fatalf("gitignore not patched correctly:\n%s", b)
	}

	// A second run is a no-op.
	again, err := Apply(root, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Action != Skipped {
			t.Errorf("second run %s: %s", r.Path, r.Action)
		}
	}
}

// The repository's templates/server (for `nix flake init -t`) must be exactly
// the embedded template rendered for "example". Skipped when the CLI is built
// on its own (e.g. the Nix package, whose source is only cli/).
func TestRepositoryTemplateMatchesEmbedded(t *testing.T) {
	repoTemplate := filepath.Join("..", "..", "..", "templates", "server")
	if _, err := os.Stat(repoTemplate); err != nil {
		t.Skip("templates/server not available")
	}
	files, err := Render(Data{Server: "example", FlakeURL: DefaultFlakeURL, PvpsVersion: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	err = filepath.WalkDir(repoTemplate, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(repoTemplate, p)
		rel = filepath.ToSlash(rel)
		onDisk[rel] = true
		want, ok := files[rel]
		if !ok {
			t.Errorf("templates/server/%s is not in the embedded template", rel)
			return nil
		}
		if have, _ := os.ReadFile(p); string(have) != string(want) {
			t.Errorf("templates/server/%s differs from the rendered embedded template", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range files {
		if !onDisk[rel] {
			t.Errorf("templates/server/%s missing (render the embedded template for \"example\")", rel)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

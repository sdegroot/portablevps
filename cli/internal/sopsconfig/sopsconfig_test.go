package sopsconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertCreatesFileAndRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sops.yaml")
	rule := Rule{PathRegex: `secrets/web\.yaml$`, Age: "age1operator,age1web"}
	if err := UpsertRule(path, rule); err != nil {
		t.Fatal(err)
	}
	rules, err := Rules(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0] != rule {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestUpsertIsIdempotentAndUpdatesAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sops.yaml")
	_ = UpsertRule(path, Rule{PathRegex: `secrets/web\.yaml$`, Age: "age1operator,age1old"})
	// same path_regex, new recipients — should update in place, not duplicate
	if err := UpsertRule(path, Rule{PathRegex: `secrets/web\.yaml$`, Age: "age1operator,age1new"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := Rules(path)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d: %+v", len(rules), rules)
	}
	if rules[0].Age != "age1operator,age1new" {
		t.Fatalf("age not updated: %+v", rules[0])
	}
}

func TestUpsertPreservesExistingRulesAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sops.yaml")
	original := `# managed sops recipients
creation_rules:
  - path_regex: secrets/test-vps\.yaml$
    age: age1operator,age1testvps
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpsertRule(path, Rule{PathRegex: `secrets/web\.yaml$`, Age: "age1operator,age1web"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := Rules(path)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d: %+v", len(rules), rules)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# managed sops recipients") {
		t.Errorf("header comment lost:\n%s", data)
	}
	if !strings.Contains(string(data), `secrets/test-vps\.yaml$`) {
		t.Errorf("existing rule lost:\n%s", data)
	}
}

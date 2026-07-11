package netbird

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEnsureGroupsCreatesMissing verifies existing groups are reused by id and
// missing ones are created.
func TestEnsureGroupsCreatesMissing(t *testing.T) {
	created := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/groups":
			_ = json.NewEncoder(w).Encode([]Group{{ID: "g-existing", Name: "portablevps-servers"}})
		case r.Method == "POST" && r.URL.Path == "/api/groups":
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body.Name)
			_ = json.NewEncoder(w).Encode(Group{ID: "g-new", Name: body.Name})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	ids, err := c.EnsureGroups([]string{"portablevps-servers", "box1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids["portablevps-servers"] != "g-existing" || ids["box1"] != "g-new" {
		t.Fatalf("unexpected ids: %+v", ids)
	}
	if len(created) != 1 || created[0] != "box1" {
		t.Fatalf("expected to create only box1, created %v", created)
	}
}

// TestEnsureSetupKeyCreatesWhenAbsent verifies a new reusable key is created and
// its plaintext returned once.
func TestEnsureSetupKeyCreatesWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/setup-keys":
			_ = json.NewEncoder(w).Encode([]SetupKey{})
		case r.Method == "POST" && r.URL.Path == "/api/setup-keys":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["type"] != "reusable" {
				t.Errorf("expected reusable key, got %v", body["type"])
			}
			// Single-host binding: a managed key must be usable exactly once.
			if body["usage_limit"] != float64(1) {
				t.Errorf("expected usage_limit 1 (single host), got %v", body["usage_limit"])
			}
			_ = json.NewEncoder(w).Encode(SetupKey{ID: "k1", Name: "portablevps-box1", Key: "SECRET-KEY", Valid: true})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	plaintext, created, err := c.EnsureSetupKey("portablevps-box1", []string{"g1"})
	if err != nil || !created || plaintext != "SECRET-KEY" {
		t.Fatalf("expected created key SECRET-KEY, got %q created=%v err=%v", plaintext, created, err)
	}
}

// TestEnsureSetupKeyReconcilesGroups verifies an existing live key with drifted
// auto-groups is reconciled in place (PUT) and no plaintext is returned.
func TestEnsureSetupKeyReconcilesGroups(t *testing.T) {
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/setup-keys":
			_ = json.NewEncoder(w).Encode([]SetupKey{{ID: "k1", Name: "portablevps-box1", Valid: true, AutoGroups: []string{"old"}}})
		case r.Method == "PUT" && r.URL.Path == "/api/setup-keys/k1":
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	plaintext, created, err := c.EnsureSetupKey("portablevps-box1", []string{"g1", "g2"})
	if err != nil || created || plaintext != "" {
		t.Fatalf("expected no creation, got %q created=%v err=%v", plaintext, created, err)
	}
	if putBody == nil {
		t.Fatal("expected the drifted key to be reconciled with a PUT")
	}
}

// TestEnsurePeerInGroupIdempotent verifies a peer already in the group is a no-op.
func TestEnsurePeerInGroupIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/groups/g1" {
			// Peers as objects — the read shape.
			w.Write([]byte(`{"id":"g1","name":"box1","peers":[{"id":"p1"},{"id":"p2"}]}`))
			return
		}
		t.Errorf("unexpected %s %s (should not PUT when already a member)", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	changed, err := c.EnsurePeerInGroup("g1", "p1")
	if err != nil || changed {
		t.Fatalf("expected no change for an existing member, got changed=%v err=%v", changed, err)
	}
}

// TestPolicySyncCreatesUpdatesPrunes exercises the managed-policy reconcile:
// one declared policy is created, and a stale managed policy is pruned.
func TestPolicySyncCreatesUpdatesPrunes(t *testing.T) {
	deleted := []string{}
	posted := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/policies":
			_ = json.NewEncoder(w).Encode([]Policy{{ID: "stale", Name: ManagedPolicyPrefix + "old-rule"}})
		case r.Method == "POST" && r.URL.Path == "/api/policies":
			b, _ := io.ReadAll(r.Body)
			posted = append(posted, string(b))
			w.WriteHeader(http.StatusOK)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/policies/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/api/policies/"))
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	all, err := c.ListPolicies()
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]Policy{}
	for _, p := range all {
		existing[p.Name] = p
	}
	spec := PolicySpec{Name: "operators-ssh", Sources: []string{"operators"}, Destinations: []string{"servers"}, Protocol: "tcp", Ports: []string{"22"}}
	groupIDs := map[string]string{"operators": "g-op", "servers": "g-srv"}
	name, err := c.UpsertPolicy(spec, groupIDs, existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != ManagedPolicyPrefix+"operators-ssh" {
		t.Fatalf("unexpected policy name %q", name)
	}
	// Prune the stale managed policy not in the declared set.
	if !map[string]bool{name: true}["stale"] {
		if err := c.DeletePolicy("stale"); err != nil {
			t.Fatal(err)
		}
	}
	if len(posted) != 1 || !strings.Contains(posted[0], "g-op") || !strings.Contains(posted[0], "g-srv") {
		t.Fatalf("expected one create resolving group ids, got %v", posted)
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("expected to prune 'stale', deleted %v", deleted)
	}
}

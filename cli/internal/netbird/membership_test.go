package netbird

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func rawIDs(ids ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		out = append(out, json.RawMessage(`{"id":"`+id+`"}`))
	}
	return out
}

// TestManagedGroupsUnionsPoliciesServersUsers verifies every group named by a
// policy, a server or a user is managed, and NetBird's All never is.
func TestManagedGroupsUnionsPoliciesServersUsers(t *testing.T) {
	fleet := FleetPolicies{
		Policies: []PolicySpec{{Sources: []string{"operators", "All"}, Destinations: []string{"portablevps-servers"}}},
		Users:    map[string][]string{"a@x": {"employees"}},
	}
	got := ManagedGroups(fleet, map[string][]string{"box1": {"portablevps-servers", "authentik", "box1"}})
	want := map[string]bool{"operators": true, "portablevps-servers": true, "employees": true, "authentik": true, "box1": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managed = %v, want %v", got, want)
	}
}

// TestPlanMembershipReconcilesDrift covers the drift seen on a live account:
// a server stuck in a stale role group, a server missing from its role group,
// a user peer that must follow its user's declared groups, an undeclared
// user's peer that must lose managed access, and unmanaged groups left alone.
func TestPlanMembershipReconcilesDrift(t *testing.T) {
	groups := []Group{
		{ID: "g-all", Name: "All", Peers: rawIDs("p-auth", "p-mon", "p-laptop", "p-old")},
		{ID: "g-web", Name: "website", Peers: rawIDs("p-auth")},              // stale: auth box in website
		{ID: "g-auth", Name: "authentik"},                                    // empty: auth box missing
		{ID: "g-ops", Name: "operators", Peers: rawIDs("p-laptop", "p-old")}, // p-old: undeclared user
		{ID: "g-k8s", Name: "kubernetes-admins", Peers: rawIDs("p-old")},     // unmanaged
		{ID: "g-users", Name: "Users"},
	}
	peers := []PeerInfo{
		{ID: "p-auth", Name: "auth-box", Hostname: "auth-box"},
		{ID: "p-mon", Name: "mon-box", Hostname: "mon-box"},
		{ID: "p-laptop", Name: "laptop", UserID: "u-me"},
		{ID: "p-old", Name: "old-laptop", UserID: "u-gone"},
	}
	users := []User{
		{ID: "u-me", Email: "Me@Example.org", Role: "owner", AutoGroups: []string{"g-users"}},
		{ID: "u-gone", Email: "gone@example.org", Role: "user", AutoGroups: []string{"g-ops"}},
	}
	want := DesiredMembership{
		Servers: map[string][]string{
			"auth-box": {"portablevps-servers", "authentik"},
			"mon-box":  {"portablevps-servers", "monitoring"},
			"web-box":  {"portablevps-servers", "website"}, // not enrolled yet
		},
		Users: map[string][]string{"me@example.org": {"operators", "employees"}, "new@example.org": {"developers"}},
	}
	want.Managed = ManagedGroups(FleetPolicies{Users: want.Users}, want.Servers)

	plan := PlanMembership(want, groups, peers, users)

	wantCreate := []string{"developers", "employees", "monitoring", "portablevps-servers"}
	if !reflect.DeepEqual(plan.CreateGroups, wantCreate) {
		t.Errorf("CreateGroups = %v, want %v", plan.CreateGroups, wantCreate)
	}

	changes := map[string]GroupChange{}
	for _, gc := range plan.Groups {
		changes[gc.Group] = gc
	}
	expect := map[string][2][]string{ // group -> {add, remove}
		"website":             {nil, {"auth-box"}},
		"authentik":           {{"auth-box"}, nil},
		"operators":           {nil, {"old-laptop"}},
		"employees":           {{"laptop"}, nil},
		"monitoring":          {{"mon-box"}, nil},
		"portablevps-servers": {{"auth-box", "mon-box"}, nil},
	}
	for g, ar := range expect {
		gc, ok := changes[g]
		if !ok {
			t.Errorf("no change planned for group %s", g)
			continue
		}
		if !reflect.DeepEqual(gc.Add, ar[0]) || !reflect.DeepEqual(gc.Remove, ar[1]) {
			t.Errorf("group %s: add %v remove %v, want add %v remove %v", g, gc.Add, gc.Remove, ar[0], ar[1])
		}
	}
	for _, g := range []string{"All", "kubernetes-admins", "Users", "developers"} {
		if _, ok := changes[g]; ok {
			t.Errorf("group %s must not change: %+v", g, changes[g])
		}
	}

	userChanges := map[string]UserChange{}
	for _, uc := range plan.Users {
		userChanges[uc.User.ID] = uc
	}
	if got := userChanges["u-me"].After; !reflect.DeepEqual(got, []string{"Users", "employees", "operators"}) {
		t.Errorf("declared user auto-groups = %v, want unmanaged Users kept + declared", got)
	}
	if got, ok := userChanges["u-gone"]; !ok || len(got.After) != 0 {
		t.Errorf("undeclared user must lose managed auto-groups, got %+v", got)
	}

	wantWarnings := 2 // new@example.org has no account; web-box has no peer
	if len(plan.Warnings) != wantWarnings {
		t.Errorf("warnings = %v, want %d", plan.Warnings, wantWarnings)
	}
}

// TestPlanMembershipNoopWhenInSync verifies an account that already matches
// the declaration yields an empty plan.
func TestPlanMembershipNoopWhenInSync(t *testing.T) {
	groups := []Group{
		{ID: "g-srv", Name: "portablevps-servers", Peers: rawIDs("p1")},
		{ID: "g-ops", Name: "operators", Peers: rawIDs("p2")},
	}
	peers := []PeerInfo{{ID: "p1", Hostname: "box1"}, {ID: "p2", Name: "laptop", UserID: "u1"}}
	users := []User{{ID: "u1", Email: "me@x", AutoGroups: []string{"g-ops"}}}
	want := DesiredMembership{
		Servers: map[string][]string{"box1": {"portablevps-servers"}},
		Users:   map[string][]string{"me@x": {"operators"}},
	}
	want.Managed = ManagedGroups(FleetPolicies{Users: want.Users}, want.Servers)
	plan := PlanMembership(want, groups, peers, users)
	if len(plan.CreateGroups)+len(plan.Groups)+len(plan.Users)+len(plan.Warnings) != 0 {
		t.Fatalf("expected empty plan, got %+v", plan)
	}
}

// TestPolicyUpToDate verifies an unchanged managed policy is recognised and
// any drift (ports, sources) is not.
func TestPolicyUpToDate(t *testing.T) {
	spec := PolicySpec{Name: "ops-ssh", Description: "d", Protocol: "tcp", Ports: []string{"22"},
		Sources: []string{"operators"}, Destinations: []string{"servers"}}
	ids := map[string]string{"operators": "g-op", "servers": "g-srv"}
	live := Policy{ID: "x", Name: ManagedPolicyPrefix + "ops-ssh", Description: "d", Enabled: true, Rules: []PolicyRule{{
		Name: ManagedPolicyPrefix + "ops-ssh", Description: "d", Enabled: true, Action: "accept", Protocol: "tcp",
		Ports: []string{"22"}, Sources: rawIDs("g-op"), Destinations: rawIDs("g-srv"),
	}}}
	if !PolicyUpToDate(live, spec, ids) {
		t.Fatal("identical policy reported as drifted")
	}
	drift := spec
	drift.Ports = []string{"22", "443"}
	if PolicyUpToDate(live, drift, ids) {
		t.Fatal("port drift not detected")
	}
	drift = spec
	drift.Sources = []string{"servers"}
	if PolicyUpToDate(live, drift, ids) {
		t.Fatal("source drift not detected")
	}
}

// TestSetUserAutoGroupsEchoesRoleAndBlock verifies the PUT carries role and
// is_blocked unchanged (NetBird requires both).
func TestSetUserAutoGroupsEchoesRoleAndBlock(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/users/u1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer srv.Close()
	c := New("tok", srv.URL)
	if err := c.SetUserAutoGroups(User{ID: "u1", Role: "user", IsBlocked: false}, []string{"g1"}); err != nil {
		t.Fatal(err)
	}
	if body["role"] != "user" || body["is_blocked"] != false || !reflect.DeepEqual(body["auto_groups"], []any{"g1"}) {
		t.Fatalf("unexpected payload %v", body)
	}
}

// TestRemovePeerFromUndeclared verifies a server leaves managed groups it no
// longer declares, and unmanaged or still-declared groups are left alone.
func TestRemovePeerFromUndeclared(t *testing.T) {
	puts := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			w.Write([]byte(`{"id":"x","name":"website","peers":[]}`))
		case "PUT":
			var body struct{ Peers []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			puts[r.URL.Path] = body.Peers
		}
	}))
	defer srv.Close()
	c := New("tok", srv.URL)
	groups := []Group{
		{ID: "g-web", Name: "website", Peers: rawIDs("p1", "p2")},
		{ID: "g-auth", Name: "authentik", Peers: rawIDs("p1")},
		{ID: "g-k8s", Name: "kubernetes-admins", Peers: rawIDs("p1")},
	}
	managed := map[string]bool{"website": true, "authentik": true}
	removed, err := c.RemovePeerFromUndeclared("p1", groups, managed, []string{"authentik"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"website"}) {
		t.Fatalf("removed %v, want [website]", removed)
	}
	if !reflect.DeepEqual(puts, map[string][]string{"/api/groups/g-web": {"p2"}}) {
		t.Fatalf("unexpected PUTs %v", puts)
	}
}

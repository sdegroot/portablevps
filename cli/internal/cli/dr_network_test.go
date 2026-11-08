package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/netbird"
)

// fakeNetbird keeps group membership in memory: group id -> set of peer ids.
type fakeNetbird struct {
	peers     []netbird.PeerInfo
	names     map[string]string // group id -> name
	members   map[string]map[string]bool
	failAdd   string // group id whose add fails
	failDel   string // group id whose removal fails
	writeLogs []string
}

func newFakeNetbird(peers []netbird.PeerInfo, groups map[string][]string) *fakeNetbird {
	f := &fakeNetbird{peers: peers, names: map[string]string{}, members: map[string]map[string]bool{}}
	for name, ids := range groups {
		id := "g-" + name
		f.names[id] = name
		f.members[id] = map[string]bool{}
		for _, p := range ids {
			f.members[id][p] = true
		}
	}
	return f
}

func (f *fakeNetbird) ListPeers() ([]netbird.PeerInfo, error) { return f.peers, nil }

func (f *fakeNetbird) ListGroups() ([]netbird.Group, error) {
	var out []netbird.Group
	for id, name := range f.names {
		var raw []json.RawMessage
		for p := range f.members[id] {
			raw = append(raw, json.RawMessage(`{"id":"`+p+`"}`))
		}
		out = append(out, netbird.Group{ID: id, Name: name, Peers: raw})
	}
	return out, nil
}

func (f *fakeNetbird) EnsurePeerInGroup(groupID, peerID string) (bool, error) {
	if groupID == f.failAdd {
		return false, fmt.Errorf("add to %s refused", groupID)
	}
	if f.members[groupID][peerID] {
		return false, nil
	}
	f.members[groupID][peerID] = true
	f.writeLogs = append(f.writeLogs, "add "+f.names[groupID])
	return true, nil
}

func (f *fakeNetbird) RemovePeerFromGroup(groupID, peerID string) (bool, error) {
	if groupID == f.failDel {
		return false, fmt.Errorf("remove from %s refused", groupID)
	}
	if !f.members[groupID][peerID] {
		return false, nil
	}
	delete(f.members[groupID], peerID)
	f.writeLogs = append(f.writeLogs, "remove "+f.names[groupID])
	return true, nil
}

func (f *fakeNetbird) groupsOf(peerID string) []string {
	var out []string
	for id, m := range f.members {
		if m[peerID] {
			out = append(out, f.names[id])
		}
	}
	sort.Strings(out)
	return out
}

var (
	hqServer    = core.Server{Name: "hq-box", Hostname: "hq-box", NetbirdName: "hq-box", NetbirdGroups: []string{"portablevps-servers", "hq", "hq-box"}}
	spareServer = core.Server{Name: "spare-box", Hostname: "spare-box", NetbirdName: "spare-box", NetbirdGroups: []string{"portablevps-servers", "spare-box"}}
)

func drillFixture(spareHostname string) *fakeNetbird {
	return newFakeNetbird(
		[]netbird.PeerInfo{
			{ID: "p-hq", Name: "hq-box", Hostname: "hq-box"},
			{ID: "p-spare", Name: "spare-box", Hostname: spareHostname},
		},
		map[string][]string{
			"portablevps-servers": {"p-hq", "p-spare"},
			"hq":                  {"p-hq"},
			"hq-box":              {"p-hq"},
			"spare-box":           {"p-spare"},
		},
	)
}

// The spare gets the source's role group, not the source's machine-name group,
// and loses exactly what it was lent; groups it already had stay.
func TestDrillNetworkLendsRoleGroupsAndTakesBackOnlyThose(t *testing.T) {
	api := drillFixture("spare-box")
	n := drillNetwork{api: api, source: hqServer, restore: spareServer}
	granted, revoke, err := n.Lend()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(granted, []string{"hq"}) {
		t.Fatalf("granted = %v, want [hq] (portablevps-servers was already there; hq-box is the source's own)", granted)
	}
	if got := api.groupsOf("p-spare"); !reflect.DeepEqual(got, []string{"hq", "portablevps-servers", "spare-box"}) {
		t.Fatalf("spare groups during the drill = %v", got)
	}
	if got := api.groupsOf("p-hq"); !reflect.DeepEqual(got, []string{"hq", "hq-box", "portablevps-servers"}) {
		t.Fatalf("the source's own peer must not change: %v", got)
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	if got := api.groupsOf("p-spare"); !reflect.DeepEqual(got, []string{"portablevps-servers", "spare-box"}) {
		t.Fatalf("spare groups after the drill = %v, want its own again", got)
	}
}

// The spare is found by its NetBird name. Mid-drill its hostname is the
// source's; a hostname lookup would find the source's own peer.
func TestDrillNetworkFindsTheSpareByNameNotHostname(t *testing.T) {
	api := drillFixture("hq-box")
	n := drillNetwork{api: api, source: hqServer, restore: spareServer}
	if _, _, err := n.Lend(); err != nil {
		t.Fatal(err)
	}
	if got := api.groupsOf("p-spare"); !reflect.DeepEqual(got, []string{"hq", "portablevps-servers", "spare-box"}) {
		t.Fatalf("lent to the wrong peer; spare groups = %v", got)
	}
}

func TestDrillNetworkUndoesAPartialLendWhenAGroupFails(t *testing.T) {
	api := drillFixture("spare-box")
	api.names["g-web"], api.members["g-web"] = "website", map[string]bool{}
	src := hqServer
	src.NetbirdGroups = []string{"portablevps-servers", "hq", "website", "hq-box"}
	api.failAdd = "g-web"
	n := drillNetwork{api: api, source: src, restore: spareServer}
	if _, _, err := n.Lend(); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("expected the add failure, got %v", err)
	}
	if got := api.groupsOf("p-spare"); !reflect.DeepEqual(got, []string{"portablevps-servers", "spare-box"}) {
		t.Fatalf("a failed lend must undo what it already added; spare groups = %v", got)
	}
}

func TestDrillNetworkRefusesAMissingGroup(t *testing.T) {
	api := drillFixture("spare-box")
	src := hqServer
	src.NetbirdGroups = []string{"hq", "not-synced-yet"}
	n := drillNetwork{api: api, source: src, restore: spareServer}
	_, _, err := n.Lend()
	if err == nil || !strings.Contains(err.Error(), "policy-sync") {
		t.Fatalf("expected a pointer to policy-sync for a missing group, got %v", err)
	}
	if got := api.groupsOf("p-spare"); !reflect.DeepEqual(got, []string{"portablevps-servers", "spare-box"}) {
		t.Fatalf("nothing may stay lent after a refused lend; spare groups = %v", got)
	}
}

func TestDrillNetworkRevokeReportsEveryFailure(t *testing.T) {
	api := drillFixture("spare-box")
	n := drillNetwork{api: api, source: hqServer, restore: spareServer}
	_, revoke, err := n.Lend()
	if err != nil {
		t.Fatal(err)
	}
	api.failDel = "g-hq"
	if err := revoke(); err == nil || !strings.Contains(err.Error(), "hq") {
		t.Fatalf("expected the failed removal to be reported, got %v", err)
	}
}

func TestDrillNetworkSpareWithoutAPeerIsAnError(t *testing.T) {
	api := drillFixture("spare-box")
	api.peers = api.peers[:1]
	n := drillNetwork{api: api, source: hqServer, restore: spareServer}
	if _, _, err := n.Lend(); err == nil || !strings.Contains(err.Error(), "spare-box") {
		t.Fatalf("expected a missing-peer error naming the spare, got %v", err)
	}
}

func TestRestoreRunsSource(t *testing.T) {
	for _, tc := range []struct {
		hostname string
		want     bool
	}{{"spare-box", false}, {"hq-box", true}} {
		n := drillNetwork{api: drillFixture(tc.hostname), source: hqServer, restore: spareServer}
		got, err := n.restoreRunsSource()
		if err != nil || got != tc.want {
			t.Errorf("spare hostname %s: restoreRunsSource = %v, %v; want %v", tc.hostname, got, err, tc.want)
		}
	}
}

func TestLendableGroupsSkipsTheSourcesOwnGroup(t *testing.T) {
	if got := lendableGroups(hqServer); !reflect.DeepEqual(got, []string{"portablevps-servers", "hq"}) {
		t.Fatalf("lendableGroups = %v", got)
	}
}

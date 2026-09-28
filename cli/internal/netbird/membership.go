package netbird

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// allGroup is NetBird's built-in group containing every peer. It is never
// managed: emptying or rewriting it would break the account.
const allGroup = "All"

// PeerInfo is a NetBird peer as listed by GET /api/peers, with the fields the
// membership reconcile needs.
type PeerInfo struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Hostname string      `json:"hostname"`
	UserID   string      `json:"user_id"`
	Groups   []GroupMini `json:"groups"`
}

// GroupMini is the {id,name} shape NetBird embeds in peers and rules.
type GroupMini struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User is a NetBird user (only the fields the CLI reads or must echo back).
type User struct {
	ID            string   `json:"id"`
	Email         string   `json:"email"`
	Role          string   `json:"role"`
	Status        string   `json:"status"`
	IsServiceUser bool     `json:"is_service_user"`
	IsBlocked     bool     `json:"is_blocked"`
	AutoGroups    []string `json:"auto_groups"`
}

// ListPeers returns every peer on the account.
func (c *Client) ListPeers() ([]PeerInfo, error) {
	var peers []PeerInfo
	if err := c.request("GET", "/api/peers", nil, &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

// ListUsers returns every user (including service users) on the account.
func (c *Client) ListUsers() ([]User, error) {
	var users []User
	if err := c.request("GET", "/api/users", nil, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// SetUserAutoGroups replaces a user's auto-groups. NetBird's PUT requires role
// and is_blocked too, so they are echoed back unchanged.
func (c *Client) SetUserAutoGroups(u User, groupIDs []string) error {
	payload := map[string]any{
		"role":        u.Role,
		"is_blocked":  u.IsBlocked,
		"auto_groups": groupIDs,
	}
	return c.request("PUT", "/api/users/"+u.ID, payload, nil)
}

// SetGroupPeers replaces a group's peer list, keeping its name and any network
// resources attached to it.
func (c *Client) SetGroupPeers(groupID string, peerIDs []string) error {
	var group struct {
		Name      string            `json:"name"`
		Resources []json.RawMessage `json:"resources"`
	}
	if err := c.request("GET", "/api/groups/"+groupID, nil, &group); err != nil {
		return err
	}
	payload := map[string]any{"name": group.Name, "peers": peerIDs}
	if len(group.Resources) > 0 {
		payload["resources"] = group.Resources
	}
	return c.request("PUT", "/api/groups/"+groupID, payload, nil)
}

// DesiredMembership is the declared intent the reconcile works from.
type DesiredMembership struct {
	// Managed is the set of group names this CLI owns. Membership of any other
	// group (console-made, NetBird's own) is never touched.
	Managed map[string]bool
	// Servers maps a server's NetBird peer name to its declared groups.
	Servers map[string][]string
	// Users maps a user's email (case-insensitive) to its declared groups.
	Users map[string][]string
}

// ManagedGroups is every group named by a policy, a server or a user, minus
// NetBird's built-in All.
func ManagedGroups(fleet FleetPolicies, serverGroups map[string][]string) map[string]bool {
	m := map[string]bool{}
	for _, p := range fleet.Policies {
		for _, g := range p.Sources {
			m[g] = true
		}
		for _, g := range p.Destinations {
			m[g] = true
		}
	}
	for _, gs := range serverGroups {
		for _, g := range gs {
			m[g] = true
		}
	}
	for _, gs := range fleet.Users {
		for _, g := range gs {
			m[g] = true
		}
	}
	delete(m, allGroup)
	return m
}

// UserChange is a planned auto-groups update for one user (group names).
type UserChange struct {
	User   User
	Before []string
	After  []string
}

// GroupChange is a planned membership update for one managed group.
type GroupChange struct {
	Group   string
	GroupID string // empty when the group does not exist yet
	Add     []string
	Remove  []string
	Peers   []string // the full desired peer-id list
}

// MembershipPlan is what the reconcile would change.
type MembershipPlan struct {
	CreateGroups []string
	Users        []UserChange
	Groups       []GroupChange
	Warnings     []string
}

// PlanMembership computes the changes that bring users' auto-groups and managed
// groups' peer lists in line with the declared intent. It is pure: it performs
// no API calls, so `--dry-run` and tests share it with the real apply.
//
// A peer belongs in a managed group when it is a declared server that lists the
// group, or when it is owned by a declared user who lists the group. Every
// other peer is removed from managed groups — that is what makes a role change
// or an offboarding actually take access away.
func PlanMembership(want DesiredMembership, groups []Group, peers []PeerInfo, users []User) MembershipPlan {
	var plan MembershipPlan

	nameToID := map[string]string{}
	idToName := map[string]string{}
	for _, g := range groups {
		nameToID[g.Name] = g.ID
		idToName[g.ID] = g.Name
	}
	for _, name := range sortedKeys(want.Managed) {
		if _, ok := nameToID[name]; !ok {
			plan.CreateGroups = append(plan.CreateGroups, name)
		}
	}

	// --- users: auto-groups = unmanaged ones kept + declared ones ---
	declaredUsers := map[string][]string{}
	for email, gs := range want.Users {
		declaredUsers[strings.ToLower(email)] = gs
	}
	userGroups := map[string][]string{} // user id -> declared group names
	seen := map[string]bool{}
	for _, u := range users {
		email := strings.ToLower(u.Email)
		declared, isDeclared := declaredUsers[email]
		if isDeclared {
			seen[email] = true
			userGroups[u.ID] = declared
		}
		before := make([]string, 0, len(u.AutoGroups))
		after := []string{}
		for _, id := range u.AutoGroups {
			name := idToName[id]
			if name == "" {
				name = id
			}
			before = append(before, name)
			if !want.Managed[name] {
				after = append(after, name)
			}
		}
		after = append(after, declared...)
		after = uniqueSorted(after)
		before = uniqueSorted(before)
		if !stringsEqual(before, after) {
			plan.Users = append(plan.Users, UserChange{User: u, Before: before, After: after})
		}
	}
	for _, email := range sortedKeys(declaredUsers) {
		if !seen[email] {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("user %s is declared but has no NetBird account yet (invite them; their groups apply once they exist)", email))
		}
	}

	// --- peers: who should be in each managed group ---
	desired := map[string]map[string]bool{} // group name -> peer ids
	for name := range want.Managed {
		desired[name] = map[string]bool{}
	}
	matchedServers := map[string]bool{}
	for _, p := range peers {
		var gs []string
		if server, ok := serverFor(p, want.Servers); ok {
			matchedServers[server] = true
			gs = want.Servers[server]
		} else if p.UserID != "" {
			gs = userGroups[p.UserID]
		}
		for _, g := range gs {
			if desired[g] != nil {
				desired[g][p.ID] = true
			}
		}
	}
	for _, server := range sortedKeys(want.Servers) {
		if !matchedServers[server] {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("server %s has no NetBird peer yet (it is grouped by its setup key when it joins)", server))
		}
	}

	current := map[string]map[string]bool{}
	for _, g := range groups {
		if !want.Managed[g.Name] {
			continue
		}
		current[g.Name] = map[string]bool{}
		for _, id := range g.peerIDs() {
			current[g.Name][id] = true
		}
	}
	peerLabel := map[string]string{}
	for _, p := range peers {
		label := p.Name
		if label == "" {
			label = p.Hostname
		}
		peerLabel[p.ID] = label
	}
	for _, name := range sortedKeys(want.Managed) {
		have := current[name]
		wantSet := desired[name]
		var add, remove []string
		for id := range wantSet {
			if !have[id] {
				add = append(add, peerLabel[id])
			}
		}
		for id := range have {
			if !wantSet[id] {
				label := peerLabel[id]
				if label == "" {
					label = id
				}
				remove = append(remove, label)
			}
		}
		if len(add) == 0 && len(remove) == 0 {
			continue
		}
		sort.Strings(add)
		sort.Strings(remove)
		plan.Groups = append(plan.Groups, GroupChange{
			Group:   name,
			GroupID: nameToID[name],
			Add:     add,
			Remove:  remove,
			Peers:   sortedKeys(wantSet),
		})
	}
	return plan
}

// serverFor returns the declared server a peer belongs to, matching on the
// peer's hostname or name like FindPeer does.
func serverFor(p PeerInfo, servers map[string][]string) (string, bool) {
	if _, ok := servers[p.Hostname]; ok && p.Hostname != "" {
		return p.Hostname, true
	}
	if _, ok := servers[p.Name]; ok && p.Name != "" {
		return p.Name, true
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		set[s] = true
	}
	return sortedKeys(set)
}

// RemovePeerFromUndeclared removes a peer from every managed group it is in but
// does not declare. It returns the names of the groups it left.
func (c *Client) RemovePeerFromUndeclared(peerID string, groups []Group, managed map[string]bool, declared []string) ([]string, error) {
	keep := map[string]bool{}
	for _, g := range declared {
		keep[g] = true
	}
	var removed []string
	for _, g := range groups {
		if !managed[g.Name] || keep[g.Name] {
			continue
		}
		ids := g.peerIDs()
		rest := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != peerID {
				rest = append(rest, id)
			}
		}
		if len(rest) == len(ids) {
			continue
		}
		if err := c.SetGroupPeers(g.ID, rest); err != nil {
			return removed, err
		}
		removed = append(removed, g.Name)
	}
	sort.Strings(removed)
	return removed, nil
}

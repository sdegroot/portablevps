package netbird

import (
	"encoding/json"
	"sort"
)

// Group is a NetBird group. Peers are returned by the API either as objects
// ({id,name,...}) or bare id strings depending on the endpoint; peerIDs handles
// both shapes.
type Group struct {
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	Peers []json.RawMessage `json:"peers"`
}

// peerIDs extracts the member peer ids regardless of whether the API returned
// objects or bare id strings.
func (g Group) peerIDs() []string {
	ids := make([]string, 0, len(g.Peers))
	for _, raw := range g.Peers {
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.ID != "" {
			ids = append(ids, obj.ID)
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			ids = append(ids, s)
		}
	}
	return ids
}

// ListGroups returns all NetBird groups.
func (c *Client) ListGroups() ([]Group, error) {
	var groups []Group
	if err := c.request("GET", "/api/groups", nil, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// EnsureGroups returns {name: id} for each requested group, creating any that
// are missing. report is called with the name of each group it creates.
func (c *Client) EnsureGroups(names []string, report func(name string)) (map[string]string, error) {
	groups, err := c.ListGroups()
	if err != nil {
		return nil, err
	}
	existing := map[string]string{}
	for _, g := range groups {
		if g.Name != "" && g.ID != "" {
			existing[g.Name] = g.ID
		}
	}
	result := make(map[string]string, len(names))
	for _, name := range names {
		if id, ok := existing[name]; ok {
			result[name] = id
			continue
		}
		if report != nil {
			report(name)
		}
		var created Group
		if err := c.request("POST", "/api/groups", map[string]any{"name": name, "peers": []string{}}, &created); err != nil {
			return nil, err
		}
		if created.ID == "" {
			return nil, &APIError{Code: 70, Msg: "failed to create NetBird group " + name}
		}
		result[name] = created.ID
	}
	return result, nil
}

// SetupKey is a NetBird setup key. The plaintext Key is only present on creation.
type SetupKey struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	Revoked    bool     `json:"revoked"`
	Valid      bool     `json:"valid"`
	AutoGroups []string `json:"auto_groups"`
}

// Bound the managed setup key rather than issuing an unlimited, year-long one.
// A key that has expired or exhausted its uses reports valid=false, so the next
// sync transparently regenerates it — and DR/repurpose always syncs before a
// host joins.
const (
	setupKeyUsageLimit = 10
	setupKeyExpiresIn  = 90 * 24 * 60 * 60 // 90 days, in seconds
)

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func stringsEqual(a, b []string) bool {
	a, b = sortedStrings(a), sortedStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// EnsureSetupKey ensures a reusable, non-revoked, still-valid setup key with the
// given name and auto-groups exists. It returns the plaintext key (only when
// newly created) and whether it created one. If a live key already exists but
// its auto-groups drifted, it reconciles them in place.
func (c *Client) EnsureSetupKey(name string, groupIDs []string) (plaintext string, created bool, err error) {
	var keys []SetupKey
	if err = c.request("GET", "/api/setup-keys", nil, &keys); err != nil {
		return "", false, err
	}
	for _, k := range keys {
		if k.Name == name && !k.Revoked && k.Valid {
			if k.ID != "" && !stringsEqual(k.AutoGroups, groupIDs) {
				payload := map[string]any{"name": name, "auto_groups": groupIDs, "revoked": false}
				if err = c.request("PUT", "/api/setup-keys/"+k.ID, payload, nil); err != nil {
					return "", false, err
				}
			}
			return "", false, nil
		}
	}
	payload := map[string]any{
		"name":        name,
		"type":        "reusable",
		"expires_in":  setupKeyExpiresIn,
		"auto_groups": groupIDs,
		"usage_limit": setupKeyUsageLimit,
		"ephemeral":   false,
	}
	var newKey SetupKey
	if err = c.request("POST", "/api/setup-keys", payload, &newKey); err != nil {
		return "", false, err
	}
	if newKey.Key == "" {
		return "", false, &APIError{Code: 70, Msg: "NetBird returned a setup key with no plaintext"}
	}
	return newKey.Key, true, nil
}

// Peer is a NetBird peer (only the fields the CLI needs).
type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

// FindPeer returns the peer whose hostname or name matches, or nil.
func (c *Client) FindPeer(name string) (*Peer, error) {
	var peers []Peer
	if err := c.request("GET", "/api/peers", nil, &peers); err != nil {
		return nil, err
	}
	for i := range peers {
		if peers[i].Hostname == name || peers[i].Name == name {
			return &peers[i], nil
		}
	}
	return nil, nil
}

// EnsurePeerInGroup adds a peer to a group (idempotent, additive). It returns
// true if it changed the group.
func (c *Client) EnsurePeerInGroup(groupID, peerID string) (bool, error) {
	var group Group
	if err := c.request("GET", "/api/groups/"+groupID, nil, &group); err != nil {
		return false, err
	}
	ids := group.peerIDs()
	for _, id := range ids {
		if id == peerID {
			return false, nil
		}
	}
	payload := map[string]any{"name": group.Name, "peers": append(ids, peerID)}
	if err := c.request("PUT", "/api/groups/"+groupID, payload, nil); err != nil {
		return false, err
	}
	return true, nil
}

// Policy is a NetBird access policy (only the fields the CLI reads).
type Policy struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Enabled     bool         `json:"enabled"`
	Rules       []PolicyRule `json:"rules"`
}

// PolicyRule mirrors a NetBird policy rule. Sources/Destinations come back as
// group objects on read but must be sent as bare group ids on write.
type PolicyRule struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Enabled       bool              `json:"enabled"`
	Action        string            `json:"action"`
	Bidirectional bool              `json:"bidirectional"`
	Protocol      string            `json:"protocol"`
	Ports         []string          `json:"ports"`
	Sources       []json.RawMessage `json:"sources"`
	Destinations  []json.RawMessage `json:"destinations"`
}

// ListPolicies returns all NetBird access policies.
func (c *Client) ListPolicies() ([]Policy, error) {
	var policies []Policy
	if err := c.request("GET", "/api/policies", nil, &policies); err != nil {
		return nil, err
	}
	return policies, nil
}

// rawGroupIDs extracts group ids from a rule's sources/destinations, which the
// API returns as objects ({id,...}) on read.
func rawGroupIDs(items []json.RawMessage) []string {
	ids := make([]string, 0, len(items))
	for _, raw := range items {
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.ID != "" {
			ids = append(ids, obj.ID)
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			ids = append(ids, s)
		}
	}
	return ids
}

// PolicySpec is a declared (desired) policy from the fleet's `.#netbird` output.
type PolicySpec struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Action        string   `json:"action"`
	Bidirectional bool     `json:"bidirectional"`
	Protocol      string   `json:"protocol"`
	Ports         []string `json:"ports"`
	Sources       []string `json:"sources"`
	Destinations  []string `json:"destinations"`
}

// ManagedPolicyPrefix marks policies this CLI owns, so it can reconcile (and
// prune) exactly its own set without touching hand-made console policies.
const ManagedPolicyPrefix = "portablevps:"

// buildPolicyPayload turns a declared spec + resolved group ids into the API
// payload NetBird expects.
func buildPolicyPayload(spec PolicySpec, groupIDs map[string]string) (map[string]any, string, error) {
	name := ManagedPolicyPrefix + spec.Name
	toIDs := func(names []string) ([]string, error) {
		ids := make([]string, 0, len(names))
		for _, n := range names {
			id, ok := groupIDs[n]
			if !ok {
				return nil, &APIError{Code: 70, Msg: "policy " + spec.Name + " references unknown group " + n}
			}
			ids = append(ids, id)
		}
		return ids, nil
	}
	sources, err := toIDs(spec.Sources)
	if err != nil {
		return nil, "", err
	}
	dests, err := toIDs(spec.Destinations)
	if err != nil {
		return nil, "", err
	}
	action := spec.Action
	if action == "" {
		action = "accept"
	}
	protocol := spec.Protocol
	if protocol == "" {
		protocol = "all"
	}
	ports := spec.Ports
	if ports == nil {
		ports = []string{}
	}
	rule := map[string]any{
		"name":          name,
		"description":   spec.Description,
		"enabled":       true,
		"action":        action,
		"bidirectional": spec.Bidirectional,
		"protocol":      protocol,
		"ports":         ports,
		"sources":       sources,
		"destinations":  dests,
	}
	return map[string]any{
		"name":        name,
		"description": spec.Description,
		"enabled":     true,
		"rules":       []any{rule},
	}, name, nil
}

// UpsertPolicy creates or updates the managed policy for spec. report is called
// with "create" or "update".
func (c *Client) UpsertPolicy(spec PolicySpec, groupIDs map[string]string, existing map[string]Policy, report func(action, name string)) (string, error) {
	payload, name, err := buildPolicyPayload(spec, groupIDs)
	if err != nil {
		return "", err
	}
	if p, ok := existing[name]; ok {
		if err := c.request("PUT", "/api/policies/"+p.ID, payload, nil); err != nil {
			return "", err
		}
		if report != nil {
			report("update", name)
		}
		return name, nil
	}
	if err := c.request("POST", "/api/policies", payload, nil); err != nil {
		return "", err
	}
	if report != nil {
		report("create", name)
	}
	return name, nil
}

// DeletePolicy removes a managed policy by id.
func (c *Client) DeletePolicy(id string) error {
	return c.request("DELETE", "/api/policies/"+id, nil, nil)
}

// SetPolicyEnabled flips a policy's enabled flag, re-sending its rules with
// group references reduced to bare ids (the API's write shape).
func (c *Client) SetPolicyEnabled(p Policy, enabled bool) error {
	rules := make([]map[string]any, 0, len(p.Rules))
	for _, r := range p.Rules {
		ports := r.Ports
		if ports == nil {
			ports = []string{}
		}
		rules = append(rules, map[string]any{
			"name":          r.Name,
			"description":   r.Description,
			"enabled":       r.Enabled,
			"action":        r.Action,
			"bidirectional": r.Bidirectional,
			"protocol":      r.Protocol,
			"ports":         ports,
			"sources":       rawGroupIDs(r.Sources),
			"destinations":  rawGroupIDs(r.Destinations),
		})
	}
	payload := map[string]any{
		"name":        p.Name,
		"description": p.Description,
		"enabled":     enabled,
		"rules":       rules,
	}
	return c.request("PUT", "/api/policies/"+p.ID, payload, nil)
}

// FindDefaultPolicy returns NetBird's built-in "Default" allow-all policy, or nil.
func (c *Client) FindDefaultPolicy() (*Policy, error) {
	policies, err := c.ListPolicies()
	if err != nil {
		return nil, err
	}
	for i := range policies {
		if policies[i].Name == "Default" {
			return &policies[i], nil
		}
	}
	return nil, nil
}

// FleetPolicies is the fleet-level `.#netbird` output.
type FleetPolicies struct {
	Policies             []PolicySpec `json:"policies"`
	DisableDefaultPolicy bool         `json:"disableDefaultPolicy"`
}

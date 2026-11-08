package cli

import (
	"errors"
	"fmt"

	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/netbird"
)

// drNetworkAPI is the part of the NetBird API a restore drill uses.
type drNetworkAPI interface {
	ListPeers() ([]netbird.PeerInfo, error)
	ListGroups() ([]netbird.Group, error)
	EnsurePeerInGroup(groupID, peerID string) (bool, error)
	RemovePeerFromGroup(groupID, peerID string) (bool, error)
}

// drillNetwork lends the source server's NetBird role groups to the restore
// host's peer for the length of a drill (core.DrillNetwork). On a default-deny
// mesh those groups are what let the restored application resolve and reach
// what it depends on, such as a private image registry.
type drillNetwork struct {
	api     drNetworkAPI
	source  core.Server // the configuration being restored
	restore core.Server // the spare's own identity
}

func netbirdNameOf(s core.Server) string {
	if s.NetbirdName != "" {
		return s.NetbirdName
	}
	return s.Name
}

func hostnameOf(s core.Server) string {
	if s.Hostname != "" {
		return s.Hostname
	}
	return s.Name
}

// lendableGroups is every group the source declares except its own
// machine-name group: that one identifies the source's machine, not its role.
func lendableGroups(source core.Server) []string {
	own := netbirdNameOf(source)
	var out []string
	for _, g := range source.NetbirdGroups {
		if g != own && g != source.Name {
			out = append(out, g)
		}
	}
	return out
}

// restorePeer finds the restore server's peer by its NetBird name only. A
// hostname match is not good enough here: during a drill the spare carries the
// source's hostname, and lending to the wrong peer would be worse than not
// lending at all.
func (n drillNetwork) restorePeer() (*netbird.PeerInfo, error) {
	peers, err := n.api.ListPeers()
	if err != nil {
		return nil, err
	}
	name := netbirdNameOf(n.restore)
	for i := range peers {
		if peers[i].Name == name {
			return &peers[i], nil
		}
	}
	return nil, fmt.Errorf("no NetBird peer is named %s", name)
}

// restoreRunsSource reports whether the restore host already runs the source's
// configuration (a re-run onto a spare an earlier drill left behind). Its
// hostname then is the source's, and it accepts only the source's admin key.
func (n drillNetwork) restoreRunsSource() (bool, error) {
	peer, err := n.restorePeer()
	if err != nil {
		return false, err
	}
	return peer.Hostname == hostnameOf(n.source) && hostnameOf(n.source) != hostnameOf(n.restore), nil
}

// Lend implements core.DrillNetwork.
func (n drillNetwork) Lend() ([]string, func() error, error) {
	want := lendableGroups(n.source)
	if len(want) == 0 {
		return nil, nil, nil
	}
	peer, err := n.restorePeer()
	if err != nil {
		return nil, nil, err
	}
	groups, err := n.api.ListGroups()
	if err != nil {
		return nil, nil, err
	}
	ids := map[string]string{}
	for _, g := range groups {
		ids[g.Name] = g.ID
	}
	type lent struct{ name, id string }
	var added []lent
	revoke := func() error {
		var errs []error
		for _, g := range added {
			if _, err := n.api.RemovePeerFromGroup(g.id, peer.ID); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", g.name, err))
			}
		}
		return errors.Join(errs...)
	}
	for _, name := range want {
		id, ok := ids[name]
		if !ok {
			err := fmt.Errorf("NetBird group %s does not exist (run `pvps network policy-sync` first)", name)
			return nil, nil, errors.Join(err, revoke())
		}
		changed, err := n.api.EnsurePeerInGroup(id, peer.ID)
		if err != nil {
			return nil, nil, errors.Join(fmt.Errorf("adding %s to %s: %w", peer.Name, name, err), revoke())
		}
		if changed {
			added = append(added, lent{name, id})
		}
	}
	names := make([]string, 0, len(added))
	for _, g := range added {
		names = append(names, g.name)
	}
	return names, revoke, nil
}

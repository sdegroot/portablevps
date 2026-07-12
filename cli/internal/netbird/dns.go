package netbird

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// planRecord is one host's generated NetBird DNS record from the proxy plan.
type planRecord struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Target string `json:"target"`
	TTL    int    `json:"ttl"`
}

type planDomain struct {
	DNS struct {
		Netbird *planRecord `json:"netbird"`
	} `json:"dns"`
}

type domainPlan struct {
	Domains []planDomain `json:"domains"`
}

// RecordsFromPlan derives the internal DNS records for zoneDomain from a host's
// proxy domain plan JSON (produced by `portablevps-proxy-domain-plan`).
func RecordsFromPlan(planJSON []byte, zoneDomain string) ([]Record, error) {
	var plan domainPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("parsing proxy domain plan: %w", err)
	}
	zone := stripDot(zoneDomain)
	var out []Record
	for _, d := range plan.Domains {
		r := d.DNS.Netbird
		if r == nil {
			continue
		}
		name := stripDot(r.Name)
		if name != zone && !strings.HasSuffix(name, "."+zone) {
			continue
		}
		ttl := r.TTL
		if ttl == 0 {
			ttl = 300
		}
		out = append(out, Record{Name: name, Type: r.Type, Content: stripDot(r.Target), TTL: ttl})
	}
	return out, nil
}

// PlanTarget returns the mesh CNAME target a host publishes for. Every record a
// host generates points at the same `<peer>.<mesh>` target, so it uniquely
// identifies records THIS host owns — the safe scope for pruning on a shared
// zone. Returns "" if the plan has no NetBird records (nothing to scope by).
func PlanTarget(planJSON []byte) (string, error) {
	var plan domainPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return "", fmt.Errorf("parsing proxy domain plan: %w", err)
	}
	for _, d := range plan.Domains {
		if r := d.DNS.Netbird; r != nil && r.Target != "" {
			return stripDot(r.Target), nil
		}
	}
	return "", nil
}

// SyncResult reports what a DNS sync did.
type SyncResult struct {
	Action string // per record: created/updated/unchanged
	Record Record
}

// DNSSync finds or creates the zone and upserts each record. When prune is set
// and ownedTarget is non-empty, it then DELETES any record whose content is
// ownedTarget (i.e. this host's records) but whose name is no longer in the
// host's plan — so removing a route from a host removes its orphaned record.
// Records pointing at other hosts, the k8s operator, or manual entries are never
// touched, because they don't match ownedTarget. report is called per action
// (created/updated/unchanged/pruned).
func (c *Client) DNSSync(zoneDomain, zoneName string, groupIDs []string, records []Record, ownedTarget string, prune bool, report func(action string, r Record)) error {
	zone, err := c.FindZone(zoneDomain)
	if err != nil {
		return err
	}
	if zone == nil {
		if zoneName == "" {
			zoneName = zoneDomain
		}
		zone, err = c.CreateZone(zoneName, zoneDomain, groupIDs)
		if err != nil {
			return err
		}
	}
	desired := make(map[string]bool, len(records))
	for _, r := range records {
		desired[stripDot(r.Name)] = true
		action, err := c.UpsertRecord(zone.ID, r)
		if err != nil {
			return err
		}
		if report != nil {
			report(action, r)
		}
	}

	if prune && ownedTarget != "" {
		existing, err := c.ListRecords(zone.ID)
		if err != nil {
			return err
		}
		target := stripDot(ownedTarget)
		for _, e := range existing {
			if stripDot(e.Content) != target || desired[stripDot(e.Name)] {
				continue
			}
			if err := c.DeleteRecord(zone.ID, e.ID); err != nil {
				return err
			}
			if report != nil {
				report("pruned", e)
			}
		}
	}
	return nil
}

// ReadPlan reads and returns proxy-domain-plan JSON bytes from an io.Reader.
func ReadPlan(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

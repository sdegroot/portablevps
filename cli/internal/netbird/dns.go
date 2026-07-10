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

// SyncResult reports what a DNS sync did.
type SyncResult struct {
	Action string // per record: created/updated/unchanged
	Record Record
}

// DNSSync finds or creates the zone and upserts each record, reporting per-record
// actions via report.
func (c *Client) DNSSync(zoneDomain, zoneName string, groupIDs []string, records []Record, report func(action string, r Record)) error {
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
	for _, r := range records {
		action, err := c.UpsertRecord(zone.ID, r)
		if err != nil {
			return err
		}
		if report != nil {
			report(action, r)
		}
	}
	return nil
}

// ReadPlan reads and returns proxy-domain-plan JSON bytes from an io.Reader.
func ReadPlan(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

package netbird

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPlanTarget extracts the host's peer CNAME target (the prune ownership key)
// from its proxy plan, even for an out-of-zone domain.
func TestPlanTarget(t *testing.T) {
	plan := []byte(`{"domains":[{"dns":{"netbird":{"name":"auth.epistola.app.","type":"CNAME","target":"boxA.epistola.int.","ttl":300}}}]}`)
	target, err := PlanTarget(plan)
	if err != nil || target != "boxA.epistola.int" {
		t.Fatalf("PlanTarget = %q, %v", target, err)
	}
}

// TestDNSSyncPrunesOnlyOwnedStale verifies pruning removes a host's own orphaned
// record (content == ownedTarget, name not in the plan) but never a record
// owned by another host (different content) or a still-desired record.
func TestDNSSyncPrunesOnlyOwnedStale(t *testing.T) {
	records := []Record{
		{ID: "r1", Name: "web.int.example.net", Type: "CNAME", Content: "boxA.mesh", TTL: 300},   // desired, keep
		{ID: "r2", Name: "old.int.example.net", Type: "CNAME", Content: "boxA.mesh", TTL: 300},   // owned + stale -> prune
		{ID: "r3", Name: "other.int.example.net", Type: "CNAME", Content: "boxB.mesh", TTL: 300}, // another host -> keep
	}
	deleted := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/dns/zones":
			_ = json.NewEncoder(w).Encode([]Zone{{ID: "z1", Name: "internal", Domain: "int.example.net"}})
		case r.Method == "GET" && r.URL.Path == "/api/dns/zones/z1/records":
			_ = json.NewEncoder(w).Encode(records)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/dns/zones/z1/records/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/api/dns/zones/z1/records/"))
			w.WriteHeader(http.StatusOK)
		case r.Method == "POST" || r.Method == "PUT":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	desired := []Record{{Name: "web.int.example.net", Type: "CNAME", Content: "boxA.mesh", TTL: 300}}
	if err := c.DNSSync("int.example.net", "internal", nil, desired, "boxA.mesh", true, nil); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "r2" {
		t.Fatalf("expected only r2 (owned+stale) pruned, deleted=%v", deleted)
	}
}

func TestRecordsFromPlanFiltersByZone(t *testing.T) {
	plan := []byte(`{"domains":[
		{"dns":{"netbird":{"name":"web.int.example.net.","type":"CNAME","target":"box.mesh.","ttl":300}}},
		{"dns":{"netbird":{"name":"other.elsewhere.net.","type":"CNAME","target":"x.","ttl":300}}},
		{"dns":{}}
	]}`)
	recs, err := RecordsFromPlan(plan, "int.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Name != "web.int.example.net" || recs[0].Content != "box.mesh" {
		t.Fatalf("expected one in-zone record, got %+v", recs)
	}
}

func TestRecordsFromPlanByZoneBucketsAndReportsUnmatched(t *testing.T) {
	// An internal name, a public split-horizon override in a different zone, and
	// a record in no configured zone.
	plan := []byte(`{"domains":[
		{"dns":{"netbird":{"name":"grafana.int.epistola.io.","type":"CNAME","target":"box.epistola.int.","ttl":300}}},
		{"dns":{"netbird":{"name":"auth.epistola.app.","type":"CNAME","target":"box.epistola.int.","ttl":300}}},
		{"dns":{"netbird":{"name":"stray.example.org.","type":"CNAME","target":"box.epistola.int.","ttl":300}}}
	]}`)
	byZone, unmatched, err := RecordsFromPlanByZone(plan, []string{"int.epistola.io", "epistola.app"})
	if err != nil {
		t.Fatal(err)
	}
	if got := byZone["int.epistola.io"]; len(got) != 1 || got[0].Name != "grafana.int.epistola.io" {
		t.Errorf("int.epistola.io bucket = %+v", got)
	}
	if got := byZone["epistola.app"]; len(got) != 1 || got[0].Name != "auth.epistola.app" {
		t.Errorf("epistola.app bucket = %+v", got)
	}
	if len(unmatched) != 1 || unmatched[0].Name != "stray.example.org" {
		t.Errorf("expected stray.example.org unmatched, got %+v", unmatched)
	}
}

func TestUpsertRecordCreatesAndIsIdempotent(t *testing.T) {
	var records []Record
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			_ = json.NewEncoder(w).Encode(records)
		case r.Method == "POST":
			var rec Record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = "rec1"
			records = append(records, rec)
			_ = json.NewEncoder(w).Encode(rec)
		}
	}))
	defer srv.Close()
	c := New("tok", srv.URL)

	action, err := c.UpsertRecord("z1", Record{Name: "web.int.example.net", Type: "CNAME", Content: "box.mesh", TTL: 300})
	if err != nil || action != "created" {
		t.Fatalf("first upsert: %q %v", action, err)
	}
	action, err = c.UpsertRecord("z1", Record{Name: "web.int.example.net", Type: "CNAME", Content: "box.mesh", TTL: 300})
	if err != nil || action != "unchanged" {
		t.Fatalf("second upsert should be unchanged: %q %v", action, err)
	}
}

func TestFindZoneMatchesDotInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Zone{{ID: "z1", Name: "internal", Domain: "int.example.net"}})
	}))
	defer srv.Close()
	c := New("tok", srv.URL)
	z, err := c.FindZone("int.example.net.")
	if err != nil || z == nil || z.ID != "z1" {
		t.Fatalf("expected to find zone z1, got %+v %v", z, err)
	}
}

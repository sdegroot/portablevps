package netbird

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

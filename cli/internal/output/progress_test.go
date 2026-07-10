package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProgressHumanPhaseLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, "service.migrate", "web-1", false)
	p.Phase("backup", "ok", "marker verified")
	p.Result("pass", "moved a to b", 0)

	out := buf.String()
	if !strings.Contains(out, "[service.migrate web-1] backup") || !strings.Contains(out, "marker verified") {
		t.Errorf("phase line missing: %q", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("result line missing: %q", out)
	}
}

func TestProgressJSONLEvents(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, "service.migrate", "web-1", true)
	p.Phase("backup", "ok", "marker verified", "marker", "abc123")
	p.Result("pass", "done", 0, "target", "web-2")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL events, got %d: %q", len(lines), buf.String())
	}

	var phase map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &phase); err != nil {
		t.Fatalf("phase event not JSON: %v", err)
	}
	if phase["event"] != "phase" || phase["phase"] != "backup" || phase["marker"] != "abc123" {
		t.Errorf("phase event wrong: %v", phase)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &result); err != nil {
		t.Fatalf("result event not JSON: %v", err)
	}
	if result["event"] != "result" || result["exit"] != float64(0) || result["target"] != "web-2" {
		t.Errorf("result event wrong: %v", result)
	}
}

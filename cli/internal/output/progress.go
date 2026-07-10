package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Progress reports the phases of a long, multi-step orchestration. At a terminal
// it prints greppable `[op scope] phase  status  msg` lines; under --json it
// emits the same information as a newline-delimited JSON event stream (JSONL)
// ending in one terminal `result` object, so CI can stream progress and fail
// fast.
type Progress struct {
	out   io.Writer
	op    string
	scope string
	json  bool
}

// NewProgress creates a Progress for operation op (e.g. "service.migrate") acting
// on scope (e.g. the server name); scope may be empty.
func NewProgress(out io.Writer, op, scope string, asJSON bool) *Progress {
	return &Progress{out: out, op: op, scope: scope, json: asJSON}
}

// Phase reports one step's status and an optional message. Extra key/value pairs
// are attached to the JSON event (ignored in human output).
func (p *Progress) Phase(phase, status, msg string, kv ...any) {
	if p.json {
		event := map[string]any{
			"event":  "phase",
			"op":     p.op,
			"phase":  phase,
			"status": status,
		}
		if p.scope != "" {
			event["scope"] = p.scope
		}
		if msg != "" {
			event["msg"] = msg
		}
		mergeKV(event, kv)
		p.emit(event)
		return
	}
	prefix := p.op
	if p.scope != "" {
		prefix = fmt.Sprintf("%s %s", p.op, p.scope)
	}
	line := fmt.Sprintf("[%s] %-10s %s", prefix, phase, status)
	if msg != "" {
		line += "  " + msg
	}
	fmt.Fprintln(p.out, line)
}

// Result reports the terminal outcome. In JSON mode it is the final `result`
// event; in human mode a single summary line. exitCode is carried in JSON so a
// machine never parses stderr.
func (p *Progress) Result(status, msg string, exitCode int, kv ...any) {
	if p.json {
		event := map[string]any{
			"event":  "result",
			"op":     p.op,
			"status": status,
			"exit":   exitCode,
		}
		if p.scope != "" {
			event["scope"] = p.scope
		}
		if msg != "" {
			event["msg"] = msg
		}
		mergeKV(event, kv)
		p.emit(event)
		return
	}
	label := "PASS"
	if status != "pass" && status != "ok" {
		label = "FAIL"
	}
	prefix := p.op
	if p.scope != "" {
		prefix = fmt.Sprintf("%s %s", p.op, p.scope)
	}
	line := fmt.Sprintf("[%s] %-10s", prefix, label)
	if msg != "" {
		line += "  " + msg
	}
	fmt.Fprintln(p.out, line)
}

func (p *Progress) emit(event map[string]any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(p.out, string(b))
}

func mergeKV(event map[string]any, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		event[key] = kv[i+1]
	}
}

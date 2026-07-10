// Package netbird is a small client for the NetBird management API (DNS zones
// and records today; groups/setup-keys/policies can be added the same way). It
// backs the `network` commands.
package netbird

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the NetBird management API.
type Client struct {
	Token   string
	BaseURL string // default https://api.netbird.io
	HTTP    *http.Client
}

// New returns a client with sensible defaults.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.netbird.io"
	}
	return &Client{
		Token:   token,
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError carries the CLI exit code for a NetBird API failure.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }
func (e *APIError) ExitCode() int { return e.Code }

// request performs an API call and decodes the JSON response into out (may be nil).
func (c *Client) request(method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+c.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &APIError{Code: 70, Msg: fmt.Sprintf("NetBird API %s %s failed: %v", method, path, err)}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &APIError{Code: 70, Msg: fmt.Sprintf("NetBird API %s %s failed: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return &APIError{Code: 70, Msg: fmt.Sprintf("NetBird API %s %s: bad JSON: %v", method, path, err)}
		}
	}
	return nil
}

// Zone is a NetBird DNS zone.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

// Record is a NetBird DNS record.
type Record struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

func stripDot(s string) string { return strings.TrimSuffix(s, ".") }

// FindZone returns the zone whose domain matches (dot-insensitive), or nil.
func (c *Client) FindZone(domain string) (*Zone, error) {
	var zones []Zone
	if err := c.request("GET", "/api/dns/zones", nil, &zones); err != nil {
		return nil, err
	}
	want := stripDot(domain)
	for i := range zones {
		if stripDot(zones[i].Domain) == want {
			return &zones[i], nil
		}
	}
	return nil, nil
}

// CreateZone creates a DNS zone distributed to the given group IDs.
func (c *Client) CreateZone(name, domain string, groupIDs []string) (*Zone, error) {
	if len(groupIDs) == 0 {
		return nil, &APIError{Code: 64, Msg: "NetBird DNS zone is missing and no distribution group IDs were provided (--dns-group-ids)"}
	}
	payload := map[string]any{
		"name":                 name,
		"domain":               stripDot(domain),
		"enabled":              true,
		"enable_search_domain": false,
		"distribution_groups":  groupIDs,
	}
	var zone Zone
	if err := c.request("POST", "/api/dns/zones", payload, &zone); err != nil {
		return nil, err
	}
	if zone.ID == "" {
		return nil, &APIError{Code: 70, Msg: "NetBird returned a zone with no id"}
	}
	return &zone, nil
}

// UpsertRecord creates or updates a record in a zone, returning the action taken.
func (c *Client) UpsertRecord(zoneID string, want Record) (string, error) {
	want.Name = stripDot(want.Name)
	want.Content = stripDot(want.Content)
	if want.TTL == 0 {
		want.TTL = 300
	}
	var existing []Record
	if err := c.request("GET", "/api/dns/zones/"+zoneID+"/records", nil, &existing); err != nil {
		return "", err
	}
	payload := map[string]any{"name": want.Name, "type": want.Type, "content": want.Content, "ttl": want.TTL}
	for _, e := range existing {
		if stripDot(e.Name) != want.Name {
			continue
		}
		if e.Type != want.Type {
			return "", &APIError{Code: 70, Msg: fmt.Sprintf("record %s exists with type %s, expected %s", want.Name, e.Type, want.Type)}
		}
		if stripDot(e.Content) == want.Content && e.TTL == want.TTL {
			return "unchanged", nil
		}
		if e.ID == "" {
			return "", &APIError{Code: 70, Msg: "record " + want.Name + " has no id"}
		}
		if err := c.request("PUT", "/api/dns/zones/"+zoneID+"/records/"+e.ID, payload, nil); err != nil {
			return "", err
		}
		return "updated", nil
	}
	if err := c.request("POST", "/api/dns/zones/"+zoneID+"/records", payload, nil); err != nil {
		return "", err
	}
	return "created", nil
}

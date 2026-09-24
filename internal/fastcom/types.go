// Package fastcom discovers the fast.com API token and fetches speed-test targets.
package fastcom

import (
	"bytes"
	"encoding/json"
)

// DefaultToken is the fast.com API token used when discovery fails.
const DefaultToken = "YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm"

// Location is a city/country pair as reported by the fast.com API.
type Location struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

// Client describes the caller as seen by the fast.com API.
type Client struct {
	IP       string   `json:"ip"`
	ASN      string   `json:"asn"`
	ISP      string   `json:"isp"`
	Location Location `json:"location"`
}

// UnmarshalJSON decodes the informational client object best-effort: asn may be
// a string or number, and malformed fields are left zero instead of failing.
func (c *Client) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil // not an object; keep the zero value
	}
	c.IP = jsonScalar(fields["ip"])
	c.ASN = jsonScalar(fields["asn"])
	c.ISP = jsonScalar(fields["isp"])
	_ = json.Unmarshal(fields["location"], &c.Location) // partial fill on type errors
	return nil
}

// jsonScalar returns a JSON string's value or a number/bool literal's text; "" otherwise.
func jsonScalar(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0, bytes.Equal(raw, []byte("null")), raw[0] == '{', raw[0] == '[':
		return ""
	case raw[0] == '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	default:
		return string(raw)
	}
}

// Target is one speed-test server URL returned by the fast.com API.
type Target struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Location Location `json:"location"`
}

// Response is the parsed body of the fast.com targets API.
type Response struct {
	Client  Client   `json:"client"`
	Targets []Target `json:"targets"`
}

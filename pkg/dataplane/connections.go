package dataplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Connection is the subset of a sing-box / Clash-API GET /connections record
// the observe "eye" (LOT-15) uses. The full record carries more fields (id,
// rule, start time, etc.) we don't need; json ignores the rest.
//
// Chains is the outbound path the flow took: chains[len-1] is the entry
// rule/selector (e.g. "sel-youtube"), chains[0] is the final outbound (e.g.
// "hysteria2-node" or "direct"). Upload/Download are cumulative bytes for the
// connection.
type Connection struct {
	Chains   []string           `json:"chains"`
	Upload   int64              `json:"upload"`
	Download int64              `json:"download"`
	Metadata ConnectionMetadata `json:"metadata"`
}

// ConnectionMetadata is the destination/protocol info on a connection.
type ConnectionMetadata struct {
	Host            string `json:"host"`            // sniffed SNI/host, may be ""
	DestinationIP   string `json:"destinationIP"`   // resolved/dialed dest IP
	DestinationPort string `json:"destinationPort"` // string, e.g. "443"
	Network         string `json:"network"`         // "tcp" | "udp"
}

// FinalOutbound is the last outbound in the chain (chains[0]), e.g. "direct" or
// a node tag. Empty if the chain is empty.
func (c Connection) FinalOutbound() string {
	if len(c.Chains) == 0 {
		return ""
	}
	return c.Chains[0]
}

// connectionsResponse is the /connections envelope.
type connectionsResponse struct {
	Connections []Connection `json:"connections"`
}

// Connections fetches the live connection table from sing-box's Clash-API
// (GET /connections), reusing ClashClient's base URL + Bearer-secret. It is the
// observe eye's primary data source: each record gives the outbound path
// (leak detection) plus byte counts (throughput / dead-flow detection) without
// any shell-outs.
func (c *ClashClient) Connections(ctx context.Context) ([]Connection, error) {
	resp, err := c.do(ctx, http.MethodGet, "/connections")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash: connections status %d", resp.StatusCode)
	}
	var body connectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("clash: decode connections: %w", err)
	}
	return body.Connections, nil
}

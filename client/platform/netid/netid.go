// Package netid derives a short, stable identifier for the network the client is
// currently attached to, so per-network state (the KB above all) can be kept
// separate per network instead of pooled globally. A recipe that beats the DPI on
// one network says nothing about an open café Wi-Fi, and returning to a known
// network should reuse what was already learned there rather than start cold.
//
// The identity is the default gateway's MAC address: stable for as long as you
// are on that network, the same across reconnects, and — unlike the SSID —
// distinct between two networks that happen to share a name. Where the MAC cannot
// be read (no ARP entry yet, a non-Ethernet link, a platform without `ip`), it
// falls back to a hash of the gateway IP and interface, and finally to a fixed
// id so the caller degrades to a single shared store rather than failing.
package netid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
)

// Fallback is the identity used when nothing about the network can be determined.
// It keeps the per-network machinery working (one shared store) instead of erroring.
const Fallback = "default"

// Network is what was detected: a short filesystem-safe Id plus the raw signals
// it was derived from, for logging.
type Network struct {
	Id      string // short, stable, filesystem-safe
	Gateway string // default-route next hop, "" if unknown
	IFace   string // egress interface, "" if unknown
	MAC     string // gateway hardware address, "" if it could not be read
}

// Detect returns the current network's identity. It never returns an error: an
// undetectable network degrades to Fallback.
func Detect(ctx context.Context) Network {
	gw, iface := defaultRoute(ctx)
	if gw == "" {
		return Network{Id: Fallback}
	}
	mac := gatewayMAC(ctx, gw, iface)
	n := Network{Gateway: gw, IFace: iface, MAC: mac}
	switch {
	case mac != "":
		// The MAC alone is the identity: the same router is the same network even
		// if the interface name or a re-issued gateway IP changes.
		n.Id = shortHash("mac:" + strings.ToLower(mac))
	default:
		// No MAC yet — key on where we egress instead. Weaker (two 192.168.1.1/eth0
		// networks collide) but far better than pooling everything together.
		n.Id = shortHash("route:" + gw + "/" + iface)
	}
	return n
}

// defaultRoute parses `ip route show default` for the next-hop IP and interface.
func defaultRoute(ctx context.Context) (gateway, iface string) {
	out, err := exec.CommandContext(ctx, "ip", "route", "show", "default").Output()
	if err != nil {
		return "", ""
	}
	return parseDefaultRoute(string(out))
}

// parseDefaultRoute extracts "default via <gw> dev <iface> ..." — the first such
// line wins (the lowest-metric default route is listed first).
func parseDefaultRoute(routeOutput string) (gateway, iface string) {
	for _, line := range strings.Split(routeOutput, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "default" {
			continue
		}
		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "via":
				gateway = f[i+1]
			case "dev":
				iface = f[i+1]
			}
		}
		if gateway != "" {
			return gateway, iface
		}
	}
	return "", ""
}

// gatewayMAC reads the gateway's hardware address from the neighbour table.
func gatewayMAC(ctx context.Context, gateway, iface string) string {
	args := []string{"neigh", "show", gateway}
	if iface != "" {
		args = append(args, "dev", iface)
	}
	out, err := exec.CommandContext(ctx, "ip", args...).Output()
	if err != nil {
		return ""
	}
	return parseNeighMAC(string(out))
}

// parseNeighMAC pulls the lladdr out of an `ip neigh` line, e.g.
// "192.168.1.1 dev wlp0s20f3 lladdr 3c:37:86:aa:bb:cc REACHABLE". An entry in
// FAILED/INCOMPLETE state has no lladdr and yields "".
func parseNeighMAC(neighOutput string) string {
	for _, line := range strings.Split(neighOutput, "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "lladdr" {
				return f[i+1]
			}
		}
	}
	return ""
}

// shortHash renders a stable 12-hex-char digest — enough to avoid collisions
// across the handful of networks one device sees, short enough for a filename.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

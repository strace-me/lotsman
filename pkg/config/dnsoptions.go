package config

import (
	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// DNSOptions maps a declared split-DNS section onto generator options. pool is the tag
// the "vpn" detour alias resolves to — the primary VPN group, which differs between the
// two roots' naming.
//
// It lives here, and not in one root, because BOTH roots generate a sing-box config
// from the same document and must derive the same dns block from it. When only the
// desktop client carried this mapping, `lotsmanctl generate` emitted a config with no
// dns block at all — so `sing-box check` on the CLI's output approved a deployment whose
// resolvers the service would never use. A check that validates a different config than
// the one that runs is the failure this project has an axiom about.
func DNSOptions(d *configv2.DNS, pool string) *singbox.DNSOptions {
	if d == nil {
		return nil
	}
	out := &singbox.DNSOptions{Direct: d.Direct, Zapret: d.Zapret, Final: d.Final, Strategy: d.Strategy}
	if d.FakeIP {
		// The ranges are the standard reserved blocks. A generated config must not
		// carry a range nobody asked for, so an empty one means "the defaults" rather
		// than an operator-chosen one.
		out.FakeIP = &singbox.FakeIPOptions{}
	}
	for _, s := range d.Servers {
		detour := s.Detour
		if detour == "vpn" {
			detour = pool
		}
		srv := singbox.DNSServer{
			Tag: s.Name, Type: s.Type, Server: s.Address, Port: s.Port,
			Path: s.Path, ServerName: s.ServerName, Detour: detour,
		}
		// A hostname-addressed encrypted server has to be resolved before it can
		// answer, so it borrows the declared DIRECT resolver rather than being
		// resolved by the tunnel it is itself a detour of.
		if s.Bootstrap && d.Direct != "" {
			srv.Bootstrap = d.Direct
		}
		out.Servers = append(out.Servers, srv)
	}
	return out
}

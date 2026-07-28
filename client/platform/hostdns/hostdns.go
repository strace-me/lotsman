// Package hostdns points the host's OWN resolver into the tunnel while a tun is
// up, so the machine's browser and shell resolve censored names through the
// trusted in-tunnel resolver instead of leaking to the LAN resolver the tun has
// to exclude.
//
// The problem it closes: a tun captures traffic system-wide, but the LAN resolver
// (e.g. 192.168.1.1) is deliberately excluded from the tun so LAN/SSH keeps
// working (client/core.localExcludeRoutes) — which means the host's own DNS
// queries go straight to that excluded resolver and escape the tunnel, where the
// ISP can NXDOMAIN-poison them. sing-box's `hijack-dns` only catches DNS that
// ENTERS the tun, so the excluded LAN resolver slips past it. Routed *services*
// are fine (sing-box resolves them via its dns block); the host's own apps are not.
//
// The fix is to point /etc/resolv.conf at an in-tun sentinel address (the tun's
// own gateway), so the host's DNS now enters the tun and hijack-dns delivers it to
// the trusted resolver. The real LAN resolver is captured first, so the tunnel can
// still bootstrap its DoH hostname and serve RU-direct through it (see the caller,
// which pins the sing-box `direct` DNS server to that captured IP rather than
// letting it re-read the mutated resolv.conf and loop).
//
// It is system-mutating and Linux-specific, and a botched restore leaves the host
// with no working DNS — so restore is belt-and-braces: the original file is stashed
// in a sidecar (under a runtime dir), so even a process that crashes mid-session
// restores it on the next start, and a reboot regenerates resolv.conf from the
// network manager regardless. The caller gates it on Linux + root + tun mode and
// keeps it off by default.
package hostdns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manager owns the redirect+restore of one host's /etc/resolv.conf. It is not safe
// for concurrent use; the caller drives it from the single start/stop path.
type Manager struct {
	resolvConf string // the file to mutate, normally /etc/resolv.conf
	sidecar    string // crash-safe backup of the pre-redirect file
	sentinel   string // in-tun address the host is pointed at (the tun gateway)
	log        *slog.Logger

	original []byte // in-memory copy of the pre-redirect file, belt to the sidecar
	engaged  bool
}

// New builds a Manager. sentinel is the in-tun address to point the host at (the
// tun's gateway IP, e.g. 172.19.0.1); sidecar is where the original resolv.conf is
// stashed for a crash-safe restore (a runtime path such as
// /run/lotsman/resolv.conf.orig). resolvConf defaults to /etc/resolv.conf when "".
func New(sentinel, sidecar, resolvConf string, log *slog.Logger) *Manager {
	if resolvConf == "" {
		resolvConf = "/etc/resolv.conf"
	}
	return &Manager{resolvConf: resolvConf, sidecar: sidecar, sentinel: sentinel, log: log}
}

// Capture reads the host's current resolver(s) and returns the usable ones (IPv4,
// non-sentinel, non-link-local), so the tunnel can bootstrap and serve RU-direct
// through the real resolver rather than the sentinel it is about to install. It
// first recovers from a prior crash: if a sidecar backup exists (a previous run
// redirected and never restored), it restores that first, so we capture the TRUE
// original and never mistake our own injected sentinel for the real resolver.
//
// It does NOT install the sentinel — that is Redirect, called after the tun is up
// so the host is never left without DNS in the gap.
func (m *Manager) Capture() ([]string, error) {
	// A symlinked resolv.conf (NetworkManager / systemd-resolved) must not be
	// rewritten by hand: an atomic rename replaces the LINK with a static file the
	// manager no longer owns, so DNS goes stale on the next network change. Refuse
	// rather than corrupt it — such a host needs a resolvectl/nmcli mechanism, which
	// this file-rewrite path is not.
	if fi, err := os.Lstat(m.resolvConf); err != nil {
		return nil, fmt.Errorf("hostdns: stat %s: %w", m.resolvConf, err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("hostdns: %s is a symlink (NetworkManager/systemd-resolved managed); a file-rewrite redirect would break it — not supported on this host", m.resolvConf)
	}
	if _, err := os.Stat(m.sidecar); err == nil {
		// A leftover sidecar is a crashed redirect ONLY if the live file is still our
		// sentinel. If the network manager has since rewritten resolv.conf (e.g. a
		// network change), the sidecar is stale — discard it, or we would clobber a
		// now-correct file with an old resolver and capture the wrong one as "real".
		cur, _ := os.ReadFile(m.resolvConf)
		if strings.Contains(string(cur), m.sentinel) {
			if err := m.restoreFromSidecar(); err != nil {
				return nil, fmt.Errorf("hostdns: recover leftover redirect: %w", err)
			}
			m.log.Warn("hostdns: recovered a leftover resolv.conf redirect from a previous run")
		} else {
			os.Remove(m.sidecar)
			m.log.Warn("hostdns: discarded a stale resolv.conf backup (the live file was not our redirect)")
		}
	}
	raw, err := os.ReadFile(m.resolvConf)
	if err != nil {
		return nil, fmt.Errorf("hostdns: read %s: %w", m.resolvConf, err)
	}
	m.original = raw
	return m.usableResolvers(raw), nil
}

// Redirect points the host at the in-tun sentinel. It stashes the current
// resolv.conf to the sidecar first (crash-safe restore), then writes the new file
// atomically. Call it only after Capture and after the tun is up. Idempotent.
func (m *Manager) Redirect() error {
	if m.engaged {
		return nil
	}
	if m.original == nil {
		raw, err := os.ReadFile(m.resolvConf)
		if err != nil {
			return fmt.Errorf("hostdns: read %s: %w", m.resolvConf, err)
		}
		m.original = raw
	}
	if err := os.MkdirAll(filepath.Dir(m.sidecar), 0o700); err != nil {
		return fmt.Errorf("hostdns: sidecar dir: %w", err)
	}
	if err := os.WriteFile(m.sidecar, m.original, 0o600); err != nil {
		return fmt.Errorf("hostdns: stash original: %w", err)
	}
	body := fmt.Sprintf("# Written by lotsman: host DNS redirected into the tunnel.\n"+
		"# The original is stashed at %s and restored on stop.\nnameserver %s\n", m.sidecar, m.sentinel)
	if err := writeFileAtomic(m.resolvConf, []byte(body)); err != nil {
		os.Remove(m.sidecar) // don't leave a sidecar that a later Capture reads as a leftover redirect
		return fmt.Errorf("hostdns: install sentinel: %w", err)
	}
	m.engaged = true
	m.log.Info("hostdns: host DNS redirected into the tunnel", "sentinel", m.sentinel, "resolv_conf", m.resolvConf)
	return nil
}

// Verify checks that a DNS lookup actually SUCCEEDS through the sentinel, so a
// sentinel that sing-box does not hijack (or a tunnel whose DNS is not resolving)
// is caught and the caller can revert — instead of silently leaving the host with
// no working DNS. It sends real queries to the sentinel over UDP:53 and retries a
// few times to let a just-started tunnel settle. Returns nil once one answers.
func (m *Manager) Verify(ctx context.Context) error {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", net.JoinHostPort(m.sentinel, "53"))
		},
	}
	var err error
	for i := 0; i < 3; i++ {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = r.LookupHost(lctx, "cloudflare.com")
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("hostdns: no answer through the sentinel %s after 3 tries: %w", m.sentinel, err)
}

// Restore puts back the resolv.conf captured before Redirect and clears the
// sidecar. Idempotent: a no-op if it never redirected. A failure leaves the host
// on the sentinel — which still resolves while the tun is up but breaks once it is
// down — so the error is returned for the caller to shout about, not swallowed.
func (m *Manager) Restore() error {
	if !m.engaged {
		// Might still have a sidecar from a crash recovered by a later start; leave it.
		return nil
	}
	if err := m.restoreFromSidecar(); err != nil {
		// Last resort: fall back to the in-memory copy, so a lost sidecar still restores.
		if m.original != nil {
			if werr := writeFileAtomic(m.resolvConf, m.original); werr != nil {
				return fmt.Errorf("hostdns: restore (sidecar %v; memory %w)", err, werr)
			}
			os.Remove(m.sidecar)
			m.engaged = false
			m.log.Warn("hostdns: restored host DNS from the in-memory copy (sidecar was gone)")
			return nil
		}
		return fmt.Errorf("hostdns: restore: %w", err)
	}
	m.engaged = false
	m.log.Info("hostdns: host DNS restored")
	return nil
}

// restoreFromSidecar copies the sidecar back over resolv.conf and removes it.
func (m *Manager) restoreFromSidecar() error {
	raw, err := os.ReadFile(m.sidecar)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(m.resolvConf, raw); err != nil {
		return err
	}
	return os.Remove(m.sidecar)
}

// usableResolvers returns the nameservers worth bootstrapping through: IPv4, not
// the sentinel we are about to install, not a link-local (%iface) address we can't
// dial from sing-box's own path.
func (m *Manager) usableResolvers(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ns := fields[1]
		if ns == m.sentinel || strings.Contains(ns, "%") || strings.Contains(ns, ":") {
			continue // sentinel, or a link-local / IPv6 address we can't cleanly pin
		}
		out = append(out, ns)
	}
	return out
}

// writeFileAtomic writes via a temp file in the same directory and renames, so a
// reader never sees a half-written resolv.conf. Same-dir keeps the rename on one
// filesystem. The file is world-readable (resolv.conf must be) at 0644.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".resolv.conf.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Sentinel reports the in-tun address the host is (or will be) pointed at.
func (m *Manager) Sentinel() string { return m.sentinel }

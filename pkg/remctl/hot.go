package remctl

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/strace-me/lotsman/pkg/singbox"
)

// HotActions is the LOT-34 apply backend: instead of mutating the reconcile-fed
// remediation map (which rebuilds config.json and RESTARTS sing-box — dropping ALL
// connections), it arms/reverts a service's reject-QUIC / ip-fallback by rewriting
// the LOCAL rule_set toggle files that permanent rules match. sing-box hot-reloads
// those files (since 1.10.0), so apply/revert never restarts the process.
//
// It satisfies the same Actions contract as ActiveSet, so the armed controller is
// agnostic to which backend is wired. Each Apply fully expresses the service's
// desired state in its two files (reject + fallback), so no separate map is needed.
type HotActions struct {
	Dir string // toggle-file dir (singbox RemDir / RuleSetDir)
	// RejectMatch returns what to reject for a service's QUIC: its plaintext domains
	// plus learned CDN ip_cidrs (QUIC SNI sniff is unreliable, so IPs carry it).
	RejectMatch func(service string) (domains, cidrs []string)
	// Write persists a toggle file atomically (injectable for tests). nil => atomicWrite.
	Write func(path string, b []byte) error
	Log   *slog.Logger
}

// Actions adapts HotActions to the controller's Apply/Rollback contract.
func (h HotActions) Actions() Actions {
	return Actions{Apply: h.apply, Rollback: h.rollback}
}

// apply writes the service's reject + fallback toggle files to reflect rem. A file
// gets the armed membership when its primitive is set, else an empty (inert) set —
// so the two files always describe the service's full current remediation state.
func (h HotActions) apply(service string, _ int, rem singbox.Remediation) error {
	var rqDomains, rqCIDRs []string
	if rem.RejectQUIC && h.RejectMatch != nil {
		rqDomains, rqCIDRs = h.RejectMatch(service)
	}
	if err := h.write(singbox.RemRejectTag(service), singbox.RemRuleSetSource(rqDomains, rqCIDRs)); err != nil {
		return err
	}
	if err := h.write(singbox.RemFallbackTag(service), singbox.RemRuleSetSource(nil, rem.FallbackCIDRs)); err != nil {
		return err
	}
	h.log().Info("remediation armed via rule_set hot-reload (no sing-box restart)",
		"service", service, "reject_quic", rem.RejectQUIC, "fallback_cidrs", len(rem.FallbackCIDRs))
	return nil
}

// rollback disarms both toggle files (empty rule sets match nothing).
func (h HotActions) rollback(service string) error {
	empty := singbox.RemRuleSetSource(nil, nil)
	if err := h.write(singbox.RemRejectTag(service), empty); err != nil {
		return err
	}
	if err := h.write(singbox.RemFallbackTag(service), empty); err != nil {
		return err
	}
	h.log().Info("remediation reverted via rule_set hot-reload (no sing-box restart)", "service", service)
	return nil
}

func (h HotActions) write(tag string, body []byte) error {
	path := singbox.RemFilePath(h.Dir, tag)
	if h.Write != nil {
		return h.Write(path, body)
	}
	return atomicWrite(path, body)
}

// atomicWrite writes body to path via a temp file + rename (so a reader/sing-box
// never sees a half-written rule_set).
func atomicWrite(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

func (h HotActions) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

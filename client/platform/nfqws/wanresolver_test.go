package nfqws

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/zapret"
)

// The egress interface used to be captured when the engine was built. On a laptop
// that is wrong twice: at boot the service can start before Wi-Fi associates, and
// on a roam the interface changes under a rule still naming the old one. Both end
// as nft matching `oifname` of something that is not the egress — nothing queued,
// nothing desynced, and every status downstream reading healthy.
func TestWANResolverIsConsultedNotRemembered(t *testing.T) {
	e := New("nfqws", zapret.Instance{Name: "t", QNum: 200}, zapret.NftOptions{Table: "inet t", WAN: "wlan0"}, "", slog.New(slog.DiscardHandler))

	calls := 0
	iface := "wlan0"
	e.SetWANResolver(func(context.Context) (string, error) {
		calls++
		return iface, nil
	})
	if e.wanFunc == nil {
		t.Fatal("resolver not set")
	}

	// The resolver must be consulted, and a CHANGE must disarm the installed table:
	// leaving it armed is exactly how a roam ends with a queue on the old interface.
	e.armed = true
	e.nftOpts.WAN = "wlan0"
	iface = "eth0"
	if wan, _ := e.wanFunc(context.Background()); wan != "eth0" {
		t.Fatalf("resolver returned %q", wan)
	}
	if calls == 0 {
		t.Error("resolver was never called")
	}
}

// And a resolver that cannot answer must produce an ERROR, not an install against
// an empty interface — a table matching oifname "" queues nothing and says so
// nowhere.
func TestNoEgressInterfaceIsAnError(t *testing.T) {
	e := New("nfqws", zapret.Instance{Name: "t", QNum: 200}, zapret.NftOptions{Table: "inet t"}, "", slog.New(slog.DiscardHandler))
	e.SetWANResolver(func(context.Context) (string, error) {
		return "", errors.New("no default route found")
	})
	if _, err := e.wanFunc(context.Background()); err == nil {
		t.Fatal("a missing default route must surface as an error")
	}
}

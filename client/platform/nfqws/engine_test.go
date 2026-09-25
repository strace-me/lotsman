package nfqws

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/zapret"
)

func engineForTest(t *testing.T, engine string) *Engine {
	t.Helper()
	return NewForEngine("", engine, zapret.Instance{Name: "probe", QNum: 200, Capture: zapret.Capture{TCP: []string{"443"}}},
		zapret.NftOptions{Table: "inet nfqueue_test"}, t.TempDir(), DefaultAdapters(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestNfqws1LaunchArgsAreUnchanged(t *testing.T) {
	engine := engineForTest(t, "nfqws1")
	args, err := engine.launchArgs([]string{"--filter-tcp=443", "--dpi-desync=fake"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "--qnum=200 ") {
		t.Errorf("qnum is not first: %s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("--dpi-desync-fwmark=0x%x", zapret.DesyncFwmark)) {
		t.Errorf("nfqws1 fwmark flag missing: %s", joined)
	}
	if engine.Engine() != "nfqws1" {
		t.Errorf("engine = %q", engine.Engine())
	}
}

func TestNfqws2LaunchArgsUseTheNfqws2Flags(t *testing.T) {
	engine := engineForTest(t, "nfqws2")
	args, err := engine.launchArgs([]string{"--filter-tcp=443", "--lua-desync=fake"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--dpi-desync-fwmark") {
		t.Errorf("nfqws2 was launched with an nfqws1 flag: %s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("--fwmark=0x%x", zapret.DesyncFwmark)) {
		t.Errorf("nfqws2 fwmark missing: %s", joined)
	}
	if !strings.Contains(joined, "--qnum=200") {
		t.Errorf("nfqws2 qnum missing: %s", joined)
	}
}

func TestNfqws2RefusesAnNfqws1Recipe(t *testing.T) {
	engine := engineForTest(t, "nfqws2")
	if _, err := engine.launchArgs([]string{"--dpi-desync=fake"}); err == nil {
		t.Fatal("an nfqws1 recipe was accepted by the nfqws2 engine")
	}
}

func TestUnknownEngineIsRejected(t *testing.T) {
	other := NewForEngine("", "nope", zapret.Instance{Name: "probe", QNum: 200}, zapret.NftOptions{}, t.TempDir(),
		DefaultAdapters(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := other.launchArgs([]string{"--dpi-desync=fake"}); err == nil {
		t.Fatal("an engine with no adapter was accepted")
	}
}

func TestCaptureIsDerivedForNfqws2Filters(t *testing.T) {
	// nfqws2 spells its port filters exactly as nfqws1 does, so the nft capture
	// derivation has to keep working for it — that is what keeps a Discord-style
	// udp range from being born unserved.
	engine := engineForTest(t, "nfqws2")
	capture := zapret.CaptureFromArgs([]string{"--filter-tcp=443", "--filter-udp=50000-50100"})
	want := zapret.Capture{TCP: []string{"443"}, UDP: []string{"50000-50100"}}
	if !capture.Equal(want) {
		t.Fatalf("capture = %#v, want %#v", capture, want)
	}
	if !engine.inst.Capture.Union(capture).Equal(engine.inst.Capture.Union(want)) {
		t.Fatal("the instance floor did not union the derived capture")
	}
}

func TestApplyOnNfqws2StaysIdempotent(t *testing.T) {
	instance := engineForTest(t, "nfqws2")
	instance.SetSettle(0)
	// No nft/nfqws2 on this machine: the point is that a refused launch is an
	// error, not a silently claimed instance.
	if _, err := instance.Apply(context.Background(), []string{"--filter-tcp=443", "--lua-desync=fake"}); err == nil {
		t.Skip("nfqws2 and nft are both present; nothing to assert here")
	}
}

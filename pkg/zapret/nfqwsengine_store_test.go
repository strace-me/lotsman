package zapret

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

func TestNfqws1RejectsNfqws2Arguments(t *testing.T) {
	bundle := strategystore.Bundle{
		Manifest: strategystore.Manifest{ID: "bad", Engine: "nfqws1"},
		Args:     strategystore.Args{Values: []string{"--lua-desync=fake"}},
	}
	if err := (NfqwsEngine{}).Validate(bundle); err == nil {
		t.Fatal("nfqws1 validator accepted --lua-desync")
	}
}

func TestNfqws1Prepare(t *testing.T) {
	bundle := strategystore.Bundle{
		Manifest: strategystore.Manifest{ID: "alt12", Engine: "nfqws1"},
		Args:     strategystore.Args{Values: []string{"--filter-tcp=443", "--dpi-desync=fake"}},
	}
	args, err := (NfqwsEngine{}).Prepare(bundle, strategystore.PrepareOptions{QNum: 200, Fwmark: 0x4554})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--qnum=200") || !strings.Contains(got, "--dpi-desync-fwmark=0x4554") || !strings.Contains(got, "--dpi-desync=fake") {
		t.Fatalf("prepared args = %q", got)
	}
}

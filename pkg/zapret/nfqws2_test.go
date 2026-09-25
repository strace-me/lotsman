package zapret

import (
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

func TestNfqws2RejectsNfqws1Arguments(t *testing.T) {
	bundle := strategystore.Bundle{
		Manifest: strategystore.Manifest{ID: "bad", Engine: "nfqws2"},
		Args:     strategystore.Args{Values: []string{"--dpi-desync=fake"}},
	}
	if err := (Nfqws2Engine{}).Validate(bundle); err == nil {
		t.Fatal("nfqws2 validator accepted --dpi-desync")
	}
}

func TestNfqws2Prepare(t *testing.T) {
	bundle := strategystore.Bundle{
		Manifest: strategystore.Manifest{ID: "lua-ts", Engine: "nfqws2"},
		Args:     strategystore.Args{Values: []string{"--filter-tcp=443", "--lua-desync=fake"}},
	}
	args, err := (Nfqws2Engine{}).Prepare(bundle, strategystore.PrepareOptions{QNum: 210})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "--qnum=210 --filter-tcp=443 --lua-desync=fake" {
		t.Fatalf("prepared args = %q", got)
	}
	if _, err := (Nfqws2Engine{}).Prepare(bundle, strategystore.PrepareOptions{QNum: 210, Fwmark: 0x4554}); err == nil {
		t.Fatal("nfqws2 accepted the nfqws1 fwmark option")
	}
}

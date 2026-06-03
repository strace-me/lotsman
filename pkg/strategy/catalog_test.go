package strategy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCatalogAddOrderAndResolve(t *testing.T) {
	c := NewCatalog()
	c.Add(Definition{ID: "alt12", Class: ClassZapret})
	c.Add(Definition{ID: "vpn1", Class: ClassVPN})
	c.Add(Definition{ID: "alt11", Class: ClassZapret})
	c.Add(Definition{ID: "alt12", Class: ClassZapret, Notes: "updated"}) // replace, keep position

	if got := c.ZapretIDs(); !reflect.DeepEqual(got, []string{"alt12", "alt11"}) {
		t.Errorf("ZapretIDs = %v, want [alt12 alt11] (zapret only, in order)", got)
	}
	d, ok := c.Resolve("alt12")
	if !ok || d.Notes != "updated" {
		t.Errorf("Resolve(alt12) = %+v, %v; want updated note", d, ok)
	}
	if _, ok := c.Resolve("nope"); ok {
		t.Error("Resolve(nope) should be false")
	}
}

func TestBuiltinCatalogIsZapret(t *testing.T) {
	ids := BuiltinCatalog().ZapretIDs()
	if len(ids) == 0 {
		t.Fatal("builtin catalog has no zapret strategies")
	}
	for _, id := range ids {
		if d, _ := BuiltinCatalog().Resolve(id); d.Class != ClassZapret {
			t.Errorf("builtin %q class = %q, want zapret", id, d.Class)
		}
	}
}

func TestMissingScripts(t *testing.T) {
	dir := t.TempDir()
	// alt12 has a launcher; alt11 and ghost do not.
	if err := os.WriteFile(filepath.Join(dir, "alt12.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := MissingScripts([]string{"alt12", "alt11", "ghost"}, dir)
	if !reflect.DeepEqual(missing, []string{"alt11", "ghost"}) {
		t.Errorf("missing = %v, want [alt11 ghost]", missing)
	}
	if got := MissingScripts([]string{"alt12"}, dir); got != nil {
		t.Errorf("expected none missing, got %v", got)
	}
}

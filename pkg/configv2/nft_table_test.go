package configv2

import "testing"

func TestInstanceTableDefaultsPerInstance(t *testing.T) {
	cfg, err := Parse([]byte(`
desync:
  instances:
    nfqws1-main:
      engine: nfqws1
      enabled: true
    nfqws2-main:
      engine: nfqws2
      enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	first := cfg.Desync.Instances["nfqws1-main"]
	second := cfg.Desync.Instances["nfqws2-main"]
	if first.Table != "inet nfqueue-nfqws1-main" || second.Table != "inet nfqueue-nfqws2-main" {
		t.Fatalf("tables = %q / %q", first.Table, second.Table)
	}
	if first.Table == second.Table {
		t.Error("two instances must not default to the same nft table")
	}
}

func TestInstanceTableIsKeptWhenSet(t *testing.T) {
	cfg, err := Parse([]byte(`
desync:
  instances:
    main:
      engine: nfqws1
      enabled: true
      table: inet my_desync
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Desync.Instances["main"].Table; got != "inet my_desync" {
		t.Errorf("table = %q", got)
	}
}

func TestInstanceTableRejectsGarbage(t *testing.T) {
	for _, table := range []string{"inet", "inet a b", "table inet x", "inet a; drop"} {
		if _, err := Parse([]byte("desync:\n  instances:\n    main:\n      engine: nfqws1\n      enabled: true\n      table: \"" + table + "\"\n")); err == nil {
			t.Errorf("table %q was accepted", table)
		}
	}
}

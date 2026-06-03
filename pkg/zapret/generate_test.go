package zapret

import (
	"strings"
	"testing"
)

func TestGenerateInit(t *testing.T) {
	in := Instance{
		Name: "discord-voice", QNum: 201,
		Capture:   Capture{UDP: []string{"19294-19344", "50000-50100"}},
		Connbytes: 0,
	}
	opts := DefaultInitOptions()
	opts.Nft.VPNServers = []string{"45.91.54.162"}
	got := GenerateInit(in, opts)

	for _, want := range []string{
		"#!/bin/sh /etc/rc.common",
		"USE_PROCD=1",
		"ACTIVE=/opt/zapret-lotsman/discord_voice/active.sh",
		"QNUM=201",
		"nft delete table inet zapret_discord_voice",
		"ip daddr { 45.91.54.162 } return",
		`oifname "eth0" meta l4proto udp udp dport { 19294-19344, 50000-50100 } queue num 201 bypass`,
		`procd_set_param command "$ACTIVE" "$QNUM"`,
		"procd_set_param respawn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("init missing:\n  %s\n--- got ---\n%s", want, got)
		}
	}
}

func TestInitNamingHelpers(t *testing.T) {
	in := Instance{Name: "TLS Main", QNum: 200}
	if got := InitName(in); got != "nfqws-tls_main" {
		t.Errorf("InitName = %q", got)
	}
	if got := TableName(in); got != "inet zapret_tls_main" {
		t.Errorf("TableName = %q", got)
	}
}

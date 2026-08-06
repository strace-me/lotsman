package subscription

import "testing"

// The defect this guards, measured on a real AcmeVPN subscription: 50 entries
// across 6 server addresses, where the provider selects the upstream exit by
// REALITY short ID. Three entries sharing one address came out in three different
// countries, so an identity of host:port alone discarded 44 of 50 real exits.
func TestNodeIDSeparatesExitsThatShareAnAddress(t *testing.T) {
	const base = "vless://ee29c36e@192.0.2.10:443?security=reality&pbk=CMkW&flow=xtls-rprx-vision&sid="
	nodes := []Node{
		{Protocol: ProtoVLESS, Server: "192.0.2.10", Port: 443, Raw: base + "3be8339923410338&sni=cdn2-15.yahoo.com#Вена"},
		{Protocol: ProtoVLESS, Server: "192.0.2.10", Port: 443, Raw: base + "55e6d9bd269aac46&sni=cdn6-91.yahoo.com#Прага"},
	}
	for i := range nodes {
		nodes[i].finalize("sub")
	}
	assignIDs(nodes)
	if nodes[0].ID == nodes[1].ID {
		t.Error("two entries with different REALITY short IDs got one ID — they are different exits")
	}
}

// The opposite provider (LOT-1): ONE node per address whose short_id rotates
// between pulls. Its identity must not move, or every rotation renames the node,
// rewrites the config and restarts sing-box on pure churn.
func TestNodeIDIgnoresAShortIDRotationOnALoneNode(t *testing.T) {
	pull := func(sid string) string {
		n := []Node{{Protocol: ProtoVLESS, Server: "1.2.3.4", Port: 443,
			Raw: "vless://u@1.2.3.4:443?security=reality&sid=" + sid + "#Node"}}
		n[0].finalize("sub")
		assignIDs(n)
		return n[0].ID
	}
	if pull("aaaa") != pull("bbbb") {
		t.Error("a rotated short_id changed a lone node's identity")
	}
}

// The other half of the contract: a provider re-labelling a node must not reset
// its health history.
func TestNodeIDIgnoresARename(t *testing.T) {
	const u = "vless://ee29c36e@1.2.3.4:443?security=reality&sid=aa"
	one := []Node{{Protocol: ProtoVLESS, Server: "1.2.3.4", Port: 443, Raw: u + "#Амстердам"},
		{Protocol: ProtoVLESS, Server: "1.2.3.4", Port: 443, Raw: u + "&sid=bb#Другой"}}
	two := []Node{{Protocol: ProtoVLESS, Server: "1.2.3.4", Port: 443, Raw: u + "#Amsterdam, NL"},
		{Protocol: ProtoVLESS, Server: "1.2.3.4", Port: 443, Raw: u + "&sid=bb#Other"}}
	for i := range one {
		one[i].finalize("s")
		two[i].finalize("s")
	}
	assignIDs(one)
	assignIDs(two)
	if one[0].ID != two[0].ID {
		t.Error("renaming a node changed its identity")
	}
}

func TestConnectionIdentityDropsTheNameFromAMarshalledNode(t *testing.T) {
	a := connectionIdentity(`{"name":"Вена","server":"1.2.3.4","port":443,"uuid":"x"}`)
	b := connectionIdentity(`{"name":"Vienna","server":"1.2.3.4","port":443,"uuid":"x"}`)
	if a != b {
		t.Errorf("the name still counts:\n a=%s\n b=%s", a, b)
	}
	c := connectionIdentity(`{"name":"Вена","server":"1.2.3.4","port":443,"uuid":"y"}`)
	if a == c {
		t.Error("a different uuid must not share an identity")
	}
}

// Junk that is neither URL nor JSON is hashed whole: over-distinguishing costs a
// node's history on a rename, under-distinguishing merges two exits.
func TestConnectionIdentityFallsBackToTheWholeString(t *testing.T) {
	if got := connectionIdentity("not-a-url-or-json"); got != "not-a-url-or-json" {
		t.Errorf("got %q, want the input unchanged", got)
	}
}

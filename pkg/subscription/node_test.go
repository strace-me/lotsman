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

// AcmeVPN turned out to be BOTH providers at once, which is what broke the
// premise the identity rule was built on: 50 nodes over 6 addresses (so the
// address alone merges them) and a fresh `sid` on EVERY fetch — 48 of 50 changed
// between two pulls three seconds apart, measured on the live subscription
// 2026-08-11. Folding sid into the ID renamed every outbound every pull, which
// rewrote the sing-box config and restarted it every five minutes, dropping every
// established TCP session with it (LOT-1, from the other side).
func TestNodeIDSurvivesRotationOnAnAddressCarryingSeveralExits(t *testing.T) {
	pull := func(sid1, sid2 string) []string {
		n := []Node{
			{Protocol: ProtoVLESS, Server: "198.51.100.10", Port: 443,
				Raw: "vless://u@198.51.100.10:443?security=reality&sni=cdn2-15.yahoo.com&sid=" + sid1 + "#Вена"},
			{Protocol: ProtoVLESS, Server: "198.51.100.10", Port: 443,
				Raw: "vless://u@198.51.100.10:443?security=reality&sni=cdn6-91.yahoo.com&sid=" + sid2 + "#Прага"},
		}
		for i := range n {
			n[i].finalize("sub")
		}
		assignIDs(n)
		return []string{n[0].ID, n[1].ID}
	}
	a := pull("3be8339923410338", "55e6d9bd269aac46")
	b := pull("ffff111122223333", "4444555566667777")
	if a[0] == a[1] {
		t.Fatal("two exits behind one address collapsed into one identity — exits would be silently dropped")
	}
	if a[0] != b[0] || a[1] != b[1] {
		t.Errorf("a rotated short_id moved the identities: %v then %v", a, b)
	}
}

// The honest fallback. When the ONLY thing telling two exits apart is the field
// that rotates, ignoring it would merge them — and merging silently deletes an
// exit you are paying for, which is the unrecoverable direction. So the full
// identity is kept for that address: the churn stays, no exit is lost, and the
// address is named in the return value.
func TestNodeIDKeepsTheRotatingFieldWhenItIsTheOnlyDiscriminator(t *testing.T) {
	n := []Node{
		{Protocol: ProtoVLESS, Server: "198.51.100.11", Port: 443,
			Raw: "vless://u@198.51.100.11:443?security=reality&sni=one.yahoo.com&sid=aaaaaaaaaaaaaaaa#Осло"},
		{Protocol: ProtoVLESS, Server: "198.51.100.11", Port: 443,
			Raw: "vless://u@198.51.100.11:443?security=reality&sni=one.yahoo.com&sid=bbbbbbbbbbbbbbbb#Рига"},
	}
	for i := range n {
		n[i].finalize("sub")
	}
	fellBack := assignIDs(n)
	if n[0].ID == n[1].ID {
		t.Fatal("two exits merged — the fallback must keep them apart even at the cost of churn")
	}
	if len(fellBack) != 1 || fellBack[0] != "198.51.100.11" {
		t.Errorf("the fallback was not reported: %v", fellBack)
	}
}

// Raw also arrives as a marshalled sing-box outbound, where the same REALITY
// value is spelled `short_id` and sits NESTED under tls.reality rather than in a
// query string. Blanking only the top level would have missed it entirely.
func TestNodeIDIgnoresANestedShortIDRotation(t *testing.T) {
	pull := func(sid string) []string {
		mk := func(sni, sid string) string {
			return `{"type":"vless","server":"198.51.100.12","server_port":443,` +
				`"tls":{"enabled":true,"server_name":"` + sni + `","reality":{"enabled":true,"short_id":"` + sid + `"}}}`
		}
		n := []Node{
			{Protocol: ProtoVLESS, Server: "198.51.100.12", Port: 443, Raw: mk("a.yahoo.com", sid)},
			{Protocol: ProtoVLESS, Server: "198.51.100.12", Port: 443, Raw: mk("b.yahoo.com", sid+"ff")},
		}
		for i := range n {
			n[i].finalize("sub")
		}
		assignIDs(n)
		return []string{n[0].ID, n[1].ID}
	}
	a, b := pull("1111"), pull("2222")
	if a[0] == a[1] {
		t.Fatal("two exits behind one address collapsed into one identity")
	}
	if a[0] != b[0] || a[1] != b[1] {
		t.Errorf("a rotated nested short_id moved the identities: %v then %v", a, b)
	}
}

// Package strategy holds strategy classes and the cold-start seed. Leaf package
// (no imports from other lotsman packages).
package strategy

// Strategy classes. A class determines which executor applies it.
const (
	ClassZapret    = "zapret"
	ClassByeDPI    = "byedpi" // alternative DPI-bypass engine (SOCKS desync), pluggable next to zapret
	ClassVPN       = "vpn"
	ClassEmergency = "emergency"
	ClassDirect    = "direct"
)

// BuiltinZapretSeed is the cold-start ranking of ZAPRET strategies, ordered by
// observed success in the author's blockcheck run. With an empty KB, this is
// what TopNZapret falls back to so a fresh install is not blind. The strings
// are strategy IDs; their nfqws argument sets live in strategy definitions
// (YAML/DB in a later phase).
var BuiltinZapretSeed = []string{
	"simple_fake_alt2",
	"alt10",
	"v4",
	"v1",
	"simple_fake",
	"alt11",
	"v7",
	"v5",
	"alt",
	"v9",
}

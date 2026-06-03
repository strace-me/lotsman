package strategy

import "os"

// Definition describes one strategy: how it is applied and what it addresses.
//
// A zapret definition is applied by symlinking its launcher script as active and
// restarting nfqws. Pre-provisioned strategies (e.g. Flowseal's alt12) ship as a
// ready <id>.sh; a strategy Lotsman discovers itself carries NFQWSArgs (the raw
// nfqws profile) instead, to be rendered into a launcher before use. A
// definition has a backing script, NFQWSArgs, or both.
type Definition struct {
	ID         string
	Class      string   // ClassZapret, ClassVPN, ...
	NFQWSArgs  []string // raw nfqws args for a discovered single-profile strategy (empty for script-backed)
	BlockTypes []string // tspu block types this strategy is known to beat (e.g. "tcp_reset"); best-effort metadata
	Notes      string
}

// Catalog is the set of known strategies in cold-start preference order. It is
// the source of truth for which strategies exist and their metadata — the place
// the KB ranks over and discovery (blockcheck) writes into.
type Catalog struct {
	defs  map[string]Definition
	order []string
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{defs: map[string]Definition{}}
}

// Add inserts or replaces a definition, preserving first-seen order.
func (c *Catalog) Add(d Definition) {
	if _, ok := c.defs[d.ID]; !ok {
		c.order = append(c.order, d.ID)
	}
	c.defs[d.ID] = d
}

// Resolve returns the definition for id.
func (c *Catalog) Resolve(id string) (Definition, bool) {
	d, ok := c.defs[id]
	return d, ok
}

// ZapretIDs returns the zapret strategy IDs in catalog order — the cold-start
// ranking the KB falls back to before it has observations.
func (c *Catalog) ZapretIDs() []string {
	out := make([]string, 0, len(c.order))
	for _, id := range c.order {
		if c.defs[id].Class == ClassZapret {
			out = append(out, id)
		}
	}
	return out
}

// BuiltinCatalog is the cold-start set of zapret strategies that actually exist
// on the router today (Flowseal launchers in the script dir). Discovered and
// config-declared strategies are added on top. Ordered best-first.
func BuiltinCatalog() *Catalog {
	c := NewCatalog()
	c.Add(Definition{ID: "alt12", Class: ClassZapret, Notes: "Flowseal ALT12 (zapret-discord-youtube 1.9.9a)"})
	c.Add(Definition{ID: "alt11", Class: ClassZapret, Notes: "Flowseal ALT11"})
	return c
}

// MissingScripts returns the IDs in `ids` whose launcher script is absent at
// <scriptDir>/<id>.sh — exactly the path the executor symlinks as active. A
// missing script means that symlink would dangle and restart nfqws into a broken
// config (an outage), so callers validate this before going live.
func MissingScripts(ids []string, scriptDir string) []string {
	var missing []string
	for _, id := range ids {
		path := scriptDir + "/" + id + ".sh"
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, id)
		}
	}
	return missing
}

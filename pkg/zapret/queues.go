package zapret

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// NFQUEUE roles. Every queue Lotsman binds has a ROLE; the number is DERIVED from
// the production queue by that role's offset, so the auxiliaries can never collide
// with each other by construction.
//
// The scattered numbers this replaces were not merely untidy, they collided in the
// shipped defaults: production 200, the client's test lane at production+1 = 201,
// and the prospector's own `-prospect-qnum` default of 201. Two engines on one
// nfqueue steal each other's packets, so a candidate is measured while wearing
// another recipe's name — the same "answered a different question" failure the
// lane was built to avoid.
const (
	RoleProduction = "production"
	RoleSandbox    = "sandbox"
	RoleProspect   = "prospect"
	RoleCanary     = "canary"
)

// roleOffsets must stay within a short run so the queues land in a range a router
// will carry and no nft rule diverts to by accident. They are contiguous so a
// reader can see at a glance that nothing is shared.
var roleOffsets = map[string]int{
	RoleProduction: 0,
	RoleSandbox:    1,
	RoleProspect:   2,
	RoleCanary:     3,
}

// QueueFor derives the queue number for a role from the production queue.
func QueueFor(production int, role string) (int, error) {
	offset, ok := roleOffsets[role]
	if !ok {
		return 0, fmt.Errorf("zapret: unknown queue role %q", role)
	}
	if production <= 0 {
		return 0, fmt.Errorf("zapret: production queue %d is not a usable number", production)
	}
	q := production + offset
	if q <= 0 || q > 65535 {
		return 0, fmt.Errorf("zapret: queue %d for role %q falls outside the usable range (production=%d)", q, role, production)
	}
	return q, nil
}

// QueuePlan is the full allocation, for logging and for tests that care about the
// whole set rather than one role.
type QueuePlan struct {
	Production int
	ByRole     map[string]int
}

// PlanQueues derives every role from the production queue and refuses an override
// that would collide with anything else. An operator-supplied number for an
// auxiliary role is allowed — a box may need a queue far from the run — but a
// collision is REFUSED rather than discovered as two engines fighting over one
// nfqueue.
func PlanQueues(production int, overrides map[string]int) (QueuePlan, error) {
	plan := QueuePlan{Production: production, ByRole: map[string]int{}}
	owner := map[int]string{}

	if _, ok := overrides[RoleProduction]; ok {
		return QueuePlan{}, fmt.Errorf("zapret: %q is the input, not something to override; pass it as the production queue", RoleProduction)
	}

	roles := make([]string, 0, len(roleOffsets))
	for role := range roleOffsets {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	for _, role := range roles {
		number, ok := overrides[role]
		if !ok {
			derived, err := QueueFor(production, role)
			if err != nil {
				return QueuePlan{}, err
			}
			number = derived
		}
		if other, taken := owner[number]; taken {
			return QueuePlan{}, fmt.Errorf("zapret: queue %d is claimed by both %q and %q; a packet reaches exactly one nfqueue", number, other, role)
		}
		owner[number] = role
		plan.ByRole[role] = number
	}
	return plan, nil
}

// Describe renders the plan for a startup log line, in a stable order.
func (p QueuePlan) Describe() string {
	roles := make([]string, 0, len(p.ByRole))
	for role := range p.ByRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		parts = append(parts, role+"="+strconv.Itoa(p.ByRole[role]))
	}
	return strings.Join(parts, " ")
}

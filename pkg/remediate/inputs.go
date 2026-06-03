package remediate

import (
	"net"

	"github.com/strace-me/lotsman/pkg/iplearn"
)

// InputsFromLearner builds planner Inputs from an iplearn Learner: for each
// named service it snapshots the learner's learned CDN CIDRs. A nil learner or
// an empty snapshot yields an empty CIDR map, which is fine — the planner then
// proposes reject-quic for a leak instead of ip-fallback. This is the only place
// remediate touches iplearn; Plan itself stays import-free of it.
func InputsFromLearner(learner *iplearn.Learner, services []string) Inputs {
	in := Inputs{CIDRs: make(map[string][]*net.IPNet, len(services))}
	if learner == nil {
		return in
	}
	for _, s := range services {
		if cidrs := learner.Snapshot(s); len(cidrs) > 0 {
			in.CIDRs[s] = cidrs
		}
	}
	return in
}

package iplearn

import "net"

// Coalesce reduces a CIDR set by (a) dropping any CIDR contained in another and
// (b) merging sibling pairs (two equal-length prefixes differing only in the
// last network bit) into their covering parent, repeated to a fixed point.
//
// It operates per address family and never widens beyond an actual covering
// prefix — a single observed /32 is never merged into a /31 unless its sibling
// /32 was also observed. The result is a fresh, deduplicated slice.
func Coalesce(in []*net.IPNet) []*net.IPNet {
	v4, v6 := splitFamily(in)
	out := append(coalesceFamily(v4), coalesceFamily(v6)...)
	return out
}

func splitFamily(in []*net.IPNet) (v4, v6 []*net.IPNet) {
	for _, n := range in {
		if n == nil {
			continue
		}
		if len(n.IP) == net.IPv4len {
			v4 = append(v4, dupNet(n))
		} else {
			v6 = append(v6, dupNet(n))
		}
	}
	return v4, v6
}

func coalesceFamily(nets []*net.IPNet) []*net.IPNet {
	// 1. containment dedup.
	set := map[string]*net.IPNet{}
	for _, n := range nets {
		addToSet(set, n)
	}
	work := make([]*net.IPNet, 0, len(set))
	for _, n := range set {
		work = append(work, n)
	}

	// 2. sibling merge to a fixed point.
	for {
		merged := false
		byKey := map[string]*net.IPNet{}
		for _, n := range work {
			byKey[n.String()] = n
		}
		next := map[string]*net.IPNet{}
		consumed := map[string]bool{}
		for _, n := range work {
			k := n.String()
			if consumed[k] {
				continue
			}
			sib := sibling(n)
			if sib != nil {
				if s, ok := byKey[sib.String()]; ok && !consumed[s.String()] {
					parent := widenByOne(n)
					addToSet(next, parent)
					consumed[k] = true
					consumed[s.String()] = true
					merged = true
					continue
				}
			}
			addToSet(next, n)
			consumed[k] = true
		}
		work = work[:0]
		for _, n := range next {
			work = append(work, n)
		}
		if !merged {
			break
		}
	}
	return work
}

// sibling returns the other half of n's parent prefix (flip the bit at position
// ones-1). Returns nil for a /0 or a host route's parent that would be /-1.
func sibling(n *net.IPNet) *net.IPNet {
	ones, bits := n.Mask.Size()
	if bits == 0 || ones == 0 {
		return nil
	}
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	bit := ones - 1
	byteIdx := bit / 8
	mask := byte(1 << (7 - uint(bit%8)))
	ip[byteIdx] ^= mask
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

// widenByOne returns the parent prefix of n (mask shortened by one bit, network
// address re-masked). Assumes ones >= 1.
func widenByOne(n *net.IPNet) *net.IPNet {
	ones, bits := n.Mask.Size()
	parentMask := net.CIDRMask(ones-1, bits)
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	return &net.IPNet{IP: ip.Mask(parentMask), Mask: parentMask}
}

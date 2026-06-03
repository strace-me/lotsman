// Package flowseal keeps the Flowseal (zapret-discord-youtube) strategy bundle
// up to date: it checks the latest GitHub release, compares it to what is
// installed, and (when newer) downloads and activates it by repointing the
// flowseal-current symlink. Strategy scripts reference that symlink, so an
// update is picked up on the next nfqws restart.
package flowseal

import (
	"strconv"
	"strings"
)

// CompareVersions orders Flowseal version strings like "1.9.9a", "1.9.8c",
// "1.9.5". Returns -1 if a<b, 0 if equal, +1 if a>b. Numeric components compare
// numerically; a trailing letter suffix breaks ties ("" < "a" < "b"), so
// 1.9.9 < 1.9.9a < 1.9.9b and 1.9.8c < 1.9.9.
func CompareVersions(a, b string) int {
	an, as := splitVersion(a)
	bn, bs := splitVersion(b)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var x, y int
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(as, bs)
}

// Newer reports whether latest is a strictly newer version than current.
func Newer(latest, current string) bool { return CompareVersions(latest, current) > 0 }

// splitVersion turns "1.9.9a" into ([1 9 9], "a"). A leading "v" and surrounding
// space are tolerated.
func splitVersion(s string) (nums []int, suffix string) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// trailing non-digit suffix (e.g. the "a" in 1.9.9a)
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if c >= '0' && c <= '9' {
			break
		}
		i--
	}
	suffix = s[i:]
	numPart := s[:i]
	for _, f := range strings.Split(numPart, ".") {
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	return nums, suffix
}

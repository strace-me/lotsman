package scaffold

import (
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

// TestRecommendedParses guards the shipped default: it must validate both without a
// subscription (the first-run state — the user adds one later) and with one.
func TestRecommendedParses(t *testing.T) {
	if _, err := config.Parse(Recommended(nil)); err != nil {
		t.Fatalf("recommended config (no subscription) did not validate: %v", err)
	}
	withSub := Recommended([]string{"https://example.com/sub"})
	if _, err := config.Parse(withSub); err != nil {
		t.Fatalf("recommended config (+subscription) did not validate: %v", err)
	}
}

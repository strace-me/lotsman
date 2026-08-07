package registry

import "testing"

// The order shown to the operator and the order sing-box matches in must be the
// SAME order, and it is neither alphabetical nor file order. The dashboard was
// alphabetical (the brain sorts by name because it is built from a map) while the
// route array is priority-then-name, so a first-match-wins engine was described by
// a list that did not describe it.
func TestSortRouteOrderIsPriorityThenName(t *testing.T) {
	in := []Service{
		{Name: "youtube"},
		{Name: "web-blocked", Priority: 10},
		{Name: "ru-direct", Priority: -10},
		{Name: "discord"},
		{Name: "ai"},
	}
	SortRouteOrder(in)

	want := []string{"ru-direct", "ai", "discord", "youtube", "web-blocked"}
	for i, w := range want {
		if in[i].Name != w {
			t.Fatalf("position %d is %q, want %q (order: %v)", i, in[i].Name, w, names(in))
		}
	}
}

func names(s []Service) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Name
	}
	return out
}

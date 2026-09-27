package zapret

import "testing"

// The shipped defaults used to collide: production 200, the client's test lane at
// production+1 = 201, and the prospector's default -prospect-qnum of 201. Two
// engines on one nfqueue steal each other's packets, so a candidate gets measured
// while carrying another recipe's name.
func TestDerivedQueuesNeverCollideAtTheShippedDefaults(t *testing.T) {
	plan, err := PlanQueues(200, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	for role, q := range plan.ByRole {
		if other, taken := seen[q]; taken {
			t.Fatalf("role %q and %q both bound queue %d: %s", role, other, q, plan.Describe())
		}
		seen[q] = role
	}
	if plan.ByRole[RoleSandbox] == 201 && plan.ByRole[RoleProspect] == 201 {
		t.Fatal("sandbox and prospector are on the same queue — the pre-fix default")
	}
	want := map[string]int{"production[0]": 200, RoleSandbox: 201, RoleProspect: 202, RoleCanary: 203}
	for role, number := range want {
		if plan.ByRole[role] != number {
			t.Errorf("%s = %d, want %d (%s)", role, plan.ByRole[role], number, plan.Describe())
		}
	}
}

func TestPlanQueuesRefusesACollidingOverride(t *testing.T) {
	_, err := PlanQueues(200, 1, map[string]int{RoleProspect: 200})
	if err == nil {
		t.Fatal("an auxiliary queue on the production number was accepted")
	}
	_, err = PlanQueues(200, 1, map[string]int{RoleCanary: 201})
	if err == nil {
		t.Fatal("an auxiliary queue overlapping another auxiliary was accepted")
	}
}

func TestPlanQueuesAcceptsADisjointOverride(t *testing.T) {
	// A box may genuinely need its canary far from the run.
	plan, err := PlanQueues(200, 1, map[string]int{RoleCanary: 900})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ByRole[RoleCanary] != 900 || plan.ByRole[RoleProspect] != 202 {
		t.Fatalf("override lost: %s", plan.Describe())
	}
}

func TestQueueForRejectsBadInput(t *testing.T) {
	if _, err := QueueFor(200, 1, "nonsense"); err == nil {
		t.Error("an unknown role was accepted")
	}
	if _, err := QueueFor(0, 1, RoleSandbox); err == nil {
		t.Error("production queue 0 was accepted")
	}
	if _, err := QueueFor(65535, 1, RoleCanary); err == nil {
		t.Error("an out-of-range queue was accepted")
	}
}

func TestPlanQueuesDescribeIsStable(t *testing.T) {
	plan, err := PlanQueues(200, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Describe() != plan.Describe() {
		t.Fatal("Describe is not stable across calls")
	}
	// Sorted by role name, so a log reader sees the same line every boot.
	if got := plan.Describe(); got != "canary=203 production[0]=200 prospect=202 sandbox=201" {
		t.Errorf("Describe = %q", got)
	}
}

// The bug this caught: anchoring auxiliaries to production[0] handed the SECOND
// production instance the test lane's queue, which is the same silent two-engines-
// on-one-nfqueue failure the allocator exists to remove.
func TestAuxQueuesClearEveryProductionInstance(t *testing.T) {
	plan, err := PlanQueues(200, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	for role, q := range plan.ByRole {
		if other, taken := seen[q]; taken {
			t.Fatalf("role %q and %q both bound queue %d: %s", role, other, q, plan.Describe())
		}
		seen[q] = role
	}
	if plan.ByRole["production[1]"] != 201 || plan.ByRole["production[2]"] != 202 {
		t.Fatalf("production run is not contiguous: %s", plan.Describe())
	}
	if plan.ByRole[RoleSandbox] != 203 {
		t.Errorf("sandbox = %d, want the first free queue after production (%s)", plan.ByRole[RoleSandbox], plan.Describe())
	}
}

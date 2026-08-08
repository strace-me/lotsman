package zapret

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

type recRunner struct {
	cmds []string
	fail map[string]error
}

func (r *recRunner) Run(_ context.Context, name string, args ...string) error {
	line := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	for k, err := range r.fail {
		if strings.Contains(line, k) {
			return err
		}
	}
	return nil
}

func (r *recRunner) joined() string { return strings.Join(r.cmds, " | ") }

func sandboxFor(t *testing.T, r *recRunner, launch Launcher) *Sandbox {
	t.Helper()
	return &Sandbox{
		Opts: IsolateOptions{Table: "inet lotsman_tune", WAN: "eth0", QNum: 201,
			TCP: []string{"443"}, Bytes: 12, Prio: -200},
		Bin: "/opt/nfqws", Dir: "/opt/fake", Run: r, Launch: launch, ProdQNum: 200,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// If the engine bound its queue before the table existed, marked probe traffic
// would fall straight through to production and the measurement would describe
// the incumbent strategy rather than the candidate.
func TestTableIsUpBeforeTheCandidateEngine(t *testing.T) {
	r := &recRunner{}
	var launched bool
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		if len(r.cmds) == 0 {
			t.Error("the engine started before any nft command ran")
		}
		launched = true
		return func() {}, nil
	})
	if err := s.Apply(context.Background(), []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if !launched {
		t.Fatal("the candidate never started")
	}
	if !strings.Contains(r.joined(), "nft -f") {
		t.Errorf("the sandbox table was never installed: %s", r.joined())
	}
}

// Sharing production's queue would put a candidate strategy on the household's
// real traffic — the one thing a sandbox exists to prevent.
func TestSandboxRefusesProductionsQueue(t *testing.T) {
	r := &recRunner{}
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		t.Error("an engine was launched despite the queue collision")
		return func() {}, nil
	})
	s.Opts.QNum = 200
	err := s.Apply(context.Background(), []string{"--dpi-desync=fake"})
	if err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("err = %v, want a refusal naming the collision", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("it touched nft anyway: %s", r.joined())
	}
}

// A candidate that will not start must be an error, not a silent no-op: the
// tuner would otherwise keep measuring the PREVIOUS candidate and credit this one.
func TestCandidateThatWillNotStartIsAnError(t *testing.T) {
	r := &recRunner{}
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		return nil, errors.New("could not read x.bin")
	})
	err := s.Apply(context.Background(), []string{"--dpi-desync=fake"})
	if err == nil || !strings.Contains(err.Error(), "could not read x.bin") {
		t.Fatalf("err = %v, want the engine's own words", err)
	}
}

// Each candidate replaces the last: two engines on one queue is not a measurement.
func TestApplyStopsThePreviousCandidate(t *testing.T) {
	r := &recRunner{}
	stops := 0
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		return func() { stops++ }, nil
	})
	ctx := context.Background()
	_ = s.Apply(ctx, []string{"--dpi-desync=fake"})
	_ = s.Apply(ctx, []string{"--dpi-desync=multisplit"})
	if stops != 1 {
		t.Errorf("stopped %d previous engines, want 1", stops)
	}
	// The table is installed once, not per candidate.
	if n := strings.Count(r.joined(), "nft -f"); n != 1 {
		t.Errorf("installed the table %d times, want 1", n)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if stops != 2 {
		t.Errorf("Close left the candidate running (%d stops)", stops)
	}
}

// A half-torn-down sandbox leaves either an untracked engine or a table quietly
// diverting marked packets, and the next run measures through the leftovers.
func TestCloseStopsTheEngineEvenWhenTheTableWillNotDelete(t *testing.T) {
	r := &recRunner{fail: map[string]error{"delete table": errors.New("device busy")}}
	stopped := false
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		return func() { stopped = true }, nil
	})
	ctx := context.Background()
	if err := s.Apply(ctx, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	err := s.Close(ctx)
	if !stopped {
		t.Error("the candidate engine outlived a failed teardown")
	}
	if err == nil || !strings.Contains(err.Error(), "leftover table") {
		t.Errorf("err = %v, want the leftover named", err)
	}
}

// The baseline arm applies no desync at all, and tester.Resolve runs it FIRST.
// Rejecting it as an empty strategy killed the first live search on its first
// step — the sandbox refused the one state every measurement is compared against.
func TestBaselineIsNoEngineNotAnError(t *testing.T) {
	r := &recRunner{}
	launched := 0
	s := sandboxFor(t, r, func(context.Context, string, string, []string) (func(), error) {
		launched++
		return func() {}, nil
	})
	ctx := context.Background()

	if err := s.Apply(ctx, nil); err != nil {
		t.Fatalf("the baseline was rejected: %v", err)
	}
	if launched != 0 {
		t.Error("an engine was started for the no-desync baseline")
	}
	// The table must still be up: the probe is isolated by its MARK, and with no
	// engine on the queue `flags bypass` passes marked packets through untouched.
	if !strings.Contains(r.joined(), "nft -f") {
		t.Errorf("the baseline did not install the sandbox table: %s", r.joined())
	}

	// And a candidate after a baseline still starts.
	if err := s.Apply(ctx, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if launched != 1 {
		t.Errorf("candidate launches = %d, want 1", launched)
	}
	// Back to baseline must stop it again, or the next measurement runs through
	// the previous candidate and describes it instead.
	if err := s.Apply(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

// nftReader is a runner that keeps what was actually rendered into the table,
// because the rule this test is about — `oifname` — exists only in the file and
// never in the command line.
type nftReader struct {
	recRunner
	files []string
}

func (n *nftReader) Run(ctx context.Context, name string, args ...string) error {
	if name == "nft" && len(args) == 2 && args[0] == "-f" {
		if b, err := os.ReadFile(args[1]); err == nil {
			n.files = append(n.files, string(b))
		}
	}
	return n.recRunner.Run(ctx, name, args...)
}

// The lane's table matches `oifname`, and a laptop roams. Read once at
// construction, the sandbox keeps queueing on an interface that stopped being
// the egress — nothing is diverted, every candidate reads "carried nothing at
// all", and the recipes take the blame for the lane pointing at a dead
// interface. Production's engine was fixed for exactly this (principle 16); the
// lane had the same hole one layer down.
func TestSandboxRescopesToTheCurrentEgressInterface(t *testing.T) {
	n := &nftReader{}
	s := sandboxFor(t, &n.recRunner, func(context.Context, string, string, []string) (func(), error) {
		return func() {}, nil
	})
	s.Run = n
	wan := "wlan0"
	s.WAN = func() string { return wan }
	ctx := context.Background()

	if err := s.Apply(ctx, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if len(n.files) != 1 || !strings.Contains(n.files[0], `oifname "wlan0"`) {
		t.Fatalf("the first lift did not scope to the live interface: %v", n.files)
	}

	// The machine roams, and the lane comes up again for the next candidate.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wan = "enp0s31f6"
	if err := s.Apply(ctx, []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if len(n.files) != 2 {
		t.Fatalf("the table was not reinstalled after a teardown: %d installs", len(n.files))
	}
	if !strings.Contains(n.files[1], `oifname "enp0s31f6"`) {
		t.Errorf("the lane kept the interface it was built with:\n%s", n.files[1])
	}
}

// A resolver that cannot answer must not blank the rule: `oifname ""` matches
// nothing, which is the same silent no-desync failure with a different cause.
func TestSandboxKeepsItsInterfaceWhenTheResolverIsBlank(t *testing.T) {
	n := &nftReader{}
	s := sandboxFor(t, &n.recRunner, func(context.Context, string, string, []string) (func(), error) {
		return func() {}, nil
	})
	s.Run = n
	s.WAN = func() string { return "" }
	if err := s.Apply(context.Background(), []string{"--dpi-desync=fake"}); err != nil {
		t.Fatal(err)
	}
	if len(n.files) != 1 || !strings.Contains(n.files[0], `oifname "eth0"`) {
		t.Fatalf("a blank resolver replaced a good interface: %v", n.files)
	}
}

package zapret

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

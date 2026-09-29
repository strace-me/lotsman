package throughput

import (
	"context"
	"net/http"
	"time"

	"github.com/strace-me/lotsman/pkg/burstprobe"
)

// SelectorProber is a Prober that aims a node at a shared probe path, measures,
// and puts the path back.
//
// The restore is not politeness. `sel-probe` is the one path both the canary and
// this prober would use, and sing-box selects for it by NAME: a scan that leaves
// it aimed at the node it last measured does not merely waste a pass — the next
// canary of any rule then reports on a node the operator never chose, and that
// report is written into the KB as if it were the rule's own. Measured live on
// 2026-08-19, the same way, for the canary's inbound (principle 2).
//
// So the cycle is: remember what the path was aimed at, measure, restore. On
// every exit, including the ones that return early, and on a context that has
// already been cancelled.
type SelectorProber struct {
	// Selector aims the probe path. Required.
	Selector Selector
	// ClientFor returns the HTTP client whose traffic follows the probe path.
	// It does NOT need to know the node: the selector has already been aimed.
	ClientFor func(ctx context.Context) (*http.Client, error)
	// URLs are the bodies to pull. They must have real content: a 204 returns
	// nothing, and a canary scoring it at 0 KiB/s once demoted every strategy
	// ever tried.
	URLs []string
	// TargetBytes is the per-node pull size.
	TargetBytes int64
	// Settle is how long to wait after aiming before pulling. Probes issued the
	// instant after a flip race the old path.
	Settle time.Duration
	// OnRestore reports a failed restore. A restore that errors is the dangerous
	// case, and it must be loud rather than logged once and forgotten: the probe
	// path is then aimed somewhere unintended, which is exactly the state this
	// type exists to prevent.
	OnRestore func(err error)
}

// Measure aims, pulls, and restores — in that order, with the restore on every
// path out.
func (s *SelectorProber) Measure(ctx context.Context, node string) (kbps float64, ok bool) {
	if s == nil || s.Selector == nil || s.ClientFor == nil || len(s.URLs) == 0 {
		return 0, false
	}

	previous, err := s.Selector.PointAt(ctx, node)
	if err != nil {
		return 0, false
	}
	// restore reports whether the path is back where it was. It runs on every exit.
	restore := func() bool {
		// A fresh context: the caller's may already be dead, and this is the one
		// moment that must not be skipped.
		if _, rerr := s.Selector.PointAt(context.WithoutCancel(ctx), previous); rerr != nil {
			if s.OnRestore != nil {
				s.OnRestore(rerr)
			}
			return false
		}
		return true
	}

	if s.Settle > 0 {
		timer := time.NewTimer(s.Settle)
		select {
		case <-ctx.Done():
			timer.Stop()
			restore()
			return 0, false
		case <-timer.C:
		}
	}

	client, err := s.ClientFor(ctx)
	if err != nil || client == nil {
		restore()
		return 0, false
	}

	target := s.TargetBytes
	if target <= 0 {
		target = defaultTargetBytes
	}
	q := probeThrough(client, s.URLs, target)
	restored := restore()

	// A measurement whose path was NOT put back is not a verdict about a node. The
	// bytes may have come out of whatever the path is aimed at now — which is the
	// node we were scanning, and also possibly the canary's exit. Crediting it
	// would write a number into the KB with no idea which path produced it.
	if !restored {
		return 0, false
	}
	if q.samples == 0 || q.loss >= 1 || q.short {
		return 0, false
	}
	return q.kbps, q.kbps > 0
}

const defaultTargetBytes int64 = 64 << 10

type reading struct {
	kbps    float64
	loss    float64
	short   bool
	samples int
}

func probeThrough(client *http.Client, urls []string, target int64) reading {
	q := burstprobe.Probe(context.Background(), client, urls, target, 1)
	return reading{kbps: q.GoodputKBps, loss: q.Loss, short: q.Short, samples: q.Samples}
}

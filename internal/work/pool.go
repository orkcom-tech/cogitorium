package work

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// The workers.
//
// A pool rather than one goroutine because lanes are independent: two
// workspaces have no reason to wait for each other, and a single worker would
// make them. It is deliberately small — the ceiling that matters is not this
// number but the lane rule, which already allows exactly one run per workspace.
//
// Nothing here knows what a unit does. Run is injected, so this file holds no
// engine, no HTTP and no model.

// Runner does one unit's work. Returning an error settles the unit with it;
// the unit is settled either way, because a unit that ran is finished whatever
// happened inside it — what went wrong belongs to the ledger row it filled in.
type Runner func(ctx context.Context, u Unit) error

// PoolOptions is what an operator can turn. Every value has a working default,
// because an install that has to be configured before it works is an install
// that does not work.
type PoolOptions struct {
	// Workers is how many units may run at once ACROSS lanes.
	Workers int
	// Idle is how long a worker waits after finding nothing before looking
	// again. Short enough that a delivery does not sit visibly waiting on an
	// empty install; long enough that an idle laptop is not doing a query per
	// millisecond forever.
	Idle time.Duration
	// PruneEvery and PruneOlderThan keep the table from growing without bound:
	// one row per delivery and per chat turn, for as long as the install runs.
	PruneEvery     time.Duration
	PruneOlderThan time.Duration
}

func (o PoolOptions) withDefaults() PoolOptions {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Idle <= 0 {
		o.Idle = 250 * time.Millisecond
	}
	if o.PruneEvery <= 0 {
		o.PruneEvery = time.Hour
	}
	if o.PruneOlderThan <= 0 {
		o.PruneOlderThan = 7 * 24 * time.Hour
	}
	return o
}

// Pool runs queued units until it is stopped.
type Pool struct {
	store *Store
	run   Runner
	opts  PoolOptions

	stop context.CancelFunc
	done sync.WaitGroup

	// woken lets an enqueue tell the workers to look now rather than at the
	// next idle tick. It is a latency optimisation and nothing more: a worker
	// that misses a wake finds the unit on its next pass, which is why the
	// channel is buffered by one and never blocks a writer.
	woken chan struct{}
}

func NewPool(s *Store, run Runner, opts PoolOptions) *Pool {
	return &Pool{store: s, run: run, opts: opts.withDefaults(), woken: make(chan struct{}, 1)}
}

// Start clears any claim left by a previous process and then begins working.
//
// The release comes first and it is not housekeeping: a unit found claimed at
// startup belongs to a process that is gone, and until it is settled its lane
// is held by nobody — every delivery into that workspace would queue behind a
// unit that will never finish.
func (p *Pool) Start(ctx context.Context) {
	if _, err := p.store.ReleaseClaims(ctx); err != nil {
		slog.Error("could not clear work claims left by the previous process; lanes may be held by nothing", "err", err)
	}
	ctx, p.stop = context.WithCancel(ctx)
	for i := range p.opts.Workers {
		p.done.Add(1)
		go p.worker(ctx, i)
	}
	p.done.Add(1)
	go p.pruner(ctx)
	slog.Info("work queue started", "workers", p.opts.Workers)
}

// Wake asks the workers to look now. Safe to call from anywhere, including
// while the pool is stopping.
func (p *Pool) Wake() {
	select {
	case p.woken <- struct{}{}:
	default:
	}
}

// Stop ends the pool and waits for units in flight to finish.
//
// It waits rather than abandoning: a unit that is mid-model-call has spent money
// and may have written files, and dropping it would leave a claimed row whose
// only account of itself is that the server restarted.
func (p *Pool) Stop() {
	if p.stop == nil {
		return
	}
	p.stop()
	p.done.Wait()
}

func (p *Pool) worker(ctx context.Context, n int) {
	defer p.done.Done()
	for {
		u, err := p.store.Claim(ctx)
		switch {
		case errors.Is(err, ErrNotFound):
			// Nothing to do. Wait for a nudge, a tick, or the end.
			select {
			case <-ctx.Done():
				return
			case <-p.woken:
			case <-time.After(p.opts.Idle):
			}
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			slog.Error("could not claim work", "worker", n, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.opts.Idle):
			}
			continue
		}

		p.execute(ctx, u)
	}
}

// execute runs one unit and settles it, whatever happens — including a panic in
// the runner. An unsettled unit holds its lane forever, so the one thing this
// function may never do is return without writing a terminal state.
func (p *Pool) execute(ctx context.Context, u Unit) {
	settle := func(errText string) {
		// Deliberately not the worker's context: the pool may be stopping,
		// and a unit that finished must be recorded as finished even then.
		// Otherwise shutdown is how a lane gets stuck.
		if err := p.store.Settle(context.WithoutCancel(ctx), u.ID, errText); err != nil {
			slog.Error("could not settle a finished work unit; its lane stays held until restart",
				"unit", u.ID, "lane", u.Lane, "err", err)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("a work unit panicked", "unit", u.ID, "kind", u.Kind, "lane", u.Lane, "panic", r)
			settle("the run panicked")
		}
	}()

	runCtx := ctx
	if !u.Deadline.IsZero() {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, u.Deadline)
		defer cancel()
	}

	started := time.Now()
	err := p.run(runCtx, u)
	slog.Info("work unit finished", "unit", u.ID, "kind", u.Kind, "lane", u.Lane,
		"ms", time.Since(started).Milliseconds(), "err", err)
	if err != nil {
		settle(err.Error())
		return
	}
	settle("")
}

func (p *Pool) pruner(ctx context.Context) {
	defer p.done.Done()
	tick := time.NewTicker(p.opts.PruneEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n, err := p.store.Prune(context.WithoutCancel(ctx), p.opts.PruneOlderThan)
			if err != nil {
				slog.Warn("could not prune the work queue", "err", err)
				continue
			}
			if n > 0 {
				slog.Info("work queue pruned", "units", n)
			}
		}
	}
}

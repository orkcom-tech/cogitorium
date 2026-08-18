package engine

import (
	"context"
	"log/slog"
	"sync"

	"github.com/orkcom-tech/cogitorium/internal/contextstore"
)

// One read of a context document per run, and the run remembers which version
// it read.
//
// WHAT THIS REPLACED. systemPrompt fetches every document bound to an agent,
// and systemPrompt is called before EVERY model turn of EVERY agent. Each fetch
// is `contextd file get <path>` — a process spawned, a space opened, a file
// read, a process reaped. A run where an orchestrator delegates to four workers
// and each takes a handful of iterations is not a handful of reads, it is
// (agents × iterations × documents) of them: an eight-document workspace at
// four agents and eight iterations is 256 subprocesses for eight files that did
// not change. On a delivery that has a caller waiting, that is most of the
// latency, and it is latency spent re-reading the same bytes.
//
// AND IT WAS ALSO WRONG, which matters more than the cost. Nothing stopped a
// document changing halfway through a run, so the orchestrator could be working
// from one version of a house-style document and the worker it delegated to
// from another — with nothing anywhere saying so. A run is one piece of work
// and it should see one context.
//
// So: the first agent to want a document reads it, and everything else in that
// run — every later turn, every delegated worker, however deep — gets the same
// bytes and the same version. The cache lives on the turn and dies with it, so
// the next run picks up whatever the space holds then. Nothing is cached across
// runs; a stale prompt would be a worse bug than a slow one.
//
// The version is not decoration. It goes into the run's record, which is what
// makes "why did this run answer that?" answerable a week later: the documents
// that fed it, at the versions that fed it. Without it the record says which
// agent ran and what it cost, and stays silent about the one input most likely
// to have changed.

// contextSnapshot is a run's frozen view of the context space.
type contextSnapshot struct {
	mu sync.Mutex
	// docs is path → body, for what this run has actually read.
	docs map[string]string
	// files is the whole space's listing, taken once. It answers two questions
	// — which paths exist, and what version each is at — and both are asked on
	// every prompt assembly: branchDocs walks the listing to find an agent's
	// own branch, and the record wants versions.
	files  []contextstore.File
	listed bool
}

// contextDoc returns a bound document's body and the version it came from,
// reading it at most once per run.
//
// Outside a run — a UI panel asking what an agent can see, a tool listing the
// space — there is no turn to cache on, and this falls straight through to
// contextd. That path is a person waiting on one request, not a machine in a
// loop, and giving it a cache would mean deciding when to invalidate one.
func (e *Engine) contextDoc(ctx context.Context, wsID int64, path string) (string, string, error) {
	snap := e.contextSnapshotFor(wsID)
	if snap == nil {
		body, err := e.ctx.Get(ctx, path)
		return body, "", err
	}

	snap.mu.Lock()
	defer snap.mu.Unlock()

	version := ""
	for _, f := range snap.listOnce(ctx, e, wsID) {
		if f.Path == path {
			version = f.Version
			break
		}
	}
	if body, ok := snap.docs[path]; ok {
		return body, version, nil
	}
	body, err := e.ctx.Get(ctx, path)
	if err != nil {
		return "", version, err
	}
	snap.docs[path] = body
	e.noteContext(wsID, path, version)
	return body, version, nil
}

// contextList returns the space's file listing, taken at most once per run.
//
// branchDocs asks for it on every prompt assembly to find an agent's own
// branch — so before this it was one `contextd file list` per agent per
// iteration, which on the run above was thirteen listings of an unchanging
// space. Outside a run it falls straight through, same as contextDoc.
func (e *Engine) contextList(ctx context.Context, wsID int64) ([]contextstore.File, error) {
	snap := e.contextSnapshotFor(wsID)
	if snap == nil {
		return e.ctx.List(ctx)
	}
	snap.mu.Lock()
	defer snap.mu.Unlock()
	files := snap.listOnce(ctx, e, wsID)
	if files == nil {
		// Distinguishable from an empty space: the caller decides whether to
		// carry on without a branch or to say the space is unreachable.
		return nil, contextstore.ErrUnavailable
	}
	return files, nil
}

// listOnce takes the listing if it has not been taken, under snap.mu.
//
// A failure is remembered as a failure — it does not retry on every prompt in
// the run. contextd being down is a condition of the machine for the length of
// a run, not a flaky call, and retrying it per agent per iteration is how a
// broken install turns a slow run into a stalled one.
func (s *contextSnapshot) listOnce(ctx context.Context, e *Engine, wsID int64) []contextstore.File {
	if s.listed {
		return s.files
	}
	s.listed = true
	files, err := e.ctx.List(ctx)
	if err != nil {
		slog.Warn("context space could not be listed for this run; agents run without their branch and the record names no versions",
			"workspace_id", wsID, "err", err)
		return nil
	}
	s.files = files
	return files
}

func (e *Engine) contextSnapshotFor(wsID int64) *contextSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	ts, ok := e.turns[wsID]
	if !ok {
		return nil
	}
	if ts.context == nil {
		ts.context = &contextSnapshot{docs: map[string]string{}}
	}
	return ts.context
}

// noteContext records a document this run read, once per path.
func (e *Engine) noteContext(wsID int64, path, version string) {
	e.note(wsID, func(r *Record) {
		for _, c := range r.Context {
			if c.Path == path {
				return
			}
		}
		r.Context = append(r.Context, ContextRead{Path: path, Version: version})
	})
}

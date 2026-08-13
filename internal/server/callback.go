package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// Telling somebody a run finished.
//
// A delivery could only ever be answered on the connection it arrived on, which
// is fine for a caller who waits and useless for a pipeline: the thing on the
// other side wants to be told. Asynchronous delivery without this just moves
// the polling somewhere else.
//
// The callback is a WORK UNIT rather than a goroutine with a retry loop in it.
// That is the whole reason the queue was built as one mechanism: a callback
// that must survive a restart, be retried with backoff, and be visible to an
// operator is exactly a queued job, and writing a second half-queue to carry it
// would be two things to keep agreeing with each other.
//
// It runs in its OWN lane — one per run — so telling somebody about job 4471
// never waits behind job 4472's agent, and a slow listener cannot hold a
// workspace.

// callbackAttempts counts the first try. Five, with the backoff below, spans
// about ten minutes: long enough to ride out a listener being restarted, short
// enough that a run's outcome is not still being announced an hour later.
const callbackAttempts = 5

var callbackBackoff = [...]time.Duration{
	15 * time.Second, 45 * time.Second, 2 * time.Minute, 7 * time.Minute,
}

// callbackTimeout bounds one attempt. A listener is being told something, not
// asked to do work: if it cannot acknowledge in this long it is down, and the
// right answer is to try again later rather than to hold a worker.
const callbackTimeout = 20 * time.Second

type callbackArgs struct {
	RunID   int64  `json:"run_id"`
	URL     string `json:"url"`
	Attempt int    `json:"attempt"`
}

// callbackBody is what a listener receives: the ledger row, in the same shape
// GET /i/{address}/runs/{id} returns.
//
// The same shape deliberately. A pipeline that handles a callback and a
// pipeline that polls should not have to parse two things, and one shape means
// a caller can switch between the two without touching their code.
type callbackBody struct {
	Run    int64          `json:"run"`
	State  string         `json:"state"`
	Result string         `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
	Did    engine.Record  `json:"did"`
	Files  []callbackFile `json:"files,omitempty"`
	Inlet  string         `json:"inlet"`
	Task   string         `json:"task"`
}

type callbackFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	URL   string `json:"url,omitempty"`
}

// callbackAllowed reports whether this install may talk to a host at all.
//
// An EMPTY allowlist means callbacks are off, not that everything is allowed.
// The inversion matters: this is a server making outbound requests to an
// address that arrived in a task, and a task is editable by anyone who can
// reach the workspace. Defaulting to open would make "edit a task" into "make
// this server fetch a URL of my choosing".
func (s *Server) callbackAllowed(raw string) error {
	if len(s.callbackHosts) == 0 {
		return errors.New("this install has no callback_hosts configured, so callbacks are off; " +
			"list the hosts a task may notify in config.yaml")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("that is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("a callback must be http or https, not %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("a callback URL needs a host")
	}
	if !gearnet.Allows(s.callbackHosts, u.Hostname()) {
		return fmt.Errorf("%q is not among this install's callback_hosts", u.Hostname())
	}
	return nil
}

// enqueueCallback is called from the one settle funnel, for a run whose task
// named somewhere to tell.
func (s *Server) enqueueCallback(ctx context.Context, run inlet.Run, target string) {
	if err := s.callbackAllowed(target); err != nil {
		// Recorded rather than silent: an operator who typed a callback into a
		// task and hears nothing needs to be told why, and the run itself is
		// finished and must not be failed for it.
		slog.Warn("a run's callback was not sent", "run_id", run.ID, "url", target, "err", err)
		return
	}
	args, err := json.Marshal(callbackArgs{RunID: run.ID, URL: target})
	if err != nil {
		slog.Error("could not build a callback", "run_id", run.ID, "err", err)
		return
	}
	// Its own lane, and an idempotency key of the run: a run is announced once
	// however many times something tries to enqueue the announcement.
	if _, err := s.queue.Enqueue(ctx, work.Unit{
		Kind: work.KindCallback, WorkspaceID: run.WorkspaceID,
		Lane: fmt.Sprintf("cb:%d", run.ID), Args: string(args),
		IdemKey: fmt.Sprintf("run:%d", run.ID), MaxAttempts: 1,
	}); err != nil && !errors.Is(err, work.ErrDuplicate) {
		slog.Error("could not queue a callback", "run_id", run.ID, "err", err)
		return
	}
	s.pool.Wake()
}

// runCallback POSTs the run to its listener, and re-queues itself with backoff
// if that did not work.
//
// Re-queueing rather than retrying in place: a worker that slept for seven
// minutes would be a worker not doing anything else, and a retry that lived
// only in memory would not survive the restart it is most likely to be needed
// across.
func (s *Server) runCallback(ctx context.Context, u work.Unit) error {
	var args callbackArgs
	if err := json.Unmarshal([]byte(u.Args), &args); err != nil {
		return fmt.Errorf("unreadable callback arguments: %w", err)
	}
	run, err := s.inlets.GetRun(ctx, args.RunID)
	if err != nil {
		return fmt.Errorf("the run this callback is about is gone: %w", err)
	}

	body, err := json.Marshal(s.callbackBodyFor(run))
	if err != nil {
		return fmt.Errorf("could not build the callback body: %w", err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, callbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, args.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cogitorium")

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.Info("run callback delivered", "run_id", run.ID, "url", args.URL,
				"status", resp.StatusCode, "attempt", args.Attempt+1)
			return nil
		}
		err = fmt.Errorf("the listener answered %s", resp.Status)
	}

	if args.Attempt+1 >= callbackAttempts {
		slog.Error("a run's callback was given up on", "run_id", run.ID, "url", args.URL,
			"attempts", args.Attempt+1, "err", err)
		return fmt.Errorf("gave up after %d attempts: %w", args.Attempt+1, err)
	}
	wait := callbackBackoff[args.Attempt]
	slog.Warn("a run's callback failed; it will be tried again", "run_id", run.ID,
		"url", args.URL, "attempt", args.Attempt+1, "in", wait.String(), "err", err)

	next, mErr := json.Marshal(callbackArgs{RunID: args.RunID, URL: args.URL, Attempt: args.Attempt + 1})
	if mErr != nil {
		return mErr
	}
	// A fresh key per attempt, or the idempotency index would refuse the
	// retry as a repeat of the announcement it is retrying.
	if _, qErr := s.queue.Enqueue(context.WithoutCancel(ctx), work.Unit{
		Kind: work.KindCallback, WorkspaceID: run.WorkspaceID,
		Lane: u.Lane, Args: string(next), MaxAttempts: 1,
		IdemKey:  fmt.Sprintf("run:%d:try:%d", args.RunID, args.Attempt+1),
		RunAfter: time.Now().UTC().Add(wait),
	}); qErr != nil {
		return fmt.Errorf("could not queue the next callback attempt: %w", qErr)
	}
	return err
}

func (s *Server) callbackBodyFor(run inlet.Run) callbackBody {
	rec := recordFrom(run.Did)
	body := callbackBody{
		Run: run.ID, State: run.State, Result: run.Result, Error: run.Error,
		Did: rec, Inlet: run.InletAddress, Task: run.TaskName,
	}
	for _, f := range rec.Files {
		cf := callbackFile{Path: f.Path, Bytes: f.Bytes}
		if s.publicURL != "" {
			cf.URL = fmt.Sprintf("%s/i/%s/runs/%d/file?path=%s",
				s.publicURL, run.InletAddress, run.ID, url.QueryEscape(f.Path))
		}
		body.Files = append(body.Files, cf)
	}
	return body
}

// settle is the ONE place a delivery's outcome is written.
//
// Every path that finishes a run goes through here — the refusals at the door,
// the judge's verdicts, a failure inside the worker, a cancel. That is what
// makes a callback something the code cannot forget to send: there is nowhere
// else to write a terminal state from.
//
// This is the only caller of inlet.Store.SettleOrLog in the server, and that is
// a convention rather than something the compiler holds: the store is a
// separate package, so the method has to stay exported. A path that settled
// directly would work and would silently never tell anyone — which is the
// failure worth knowing about, so it is stated here rather than assumed.
func (s *Server) settle(ctx context.Context, runID int64, state, result, errText string, did engine.Record) {
	s.inlets.SettleOrLog(ctx, runID, state, result, errText, ledgerRecord(did))

	run, err := s.inlets.GetRun(ctx, runID)
	if err != nil {
		slog.Warn("could not read back a settled run to see whether anyone should be told",
			"run_id", runID, "err", err)
		return
	}
	if run.InletID == nil {
		return
	}
	task, err := s.inlets.TaskByName(ctx, *run.InletID, run.TaskName)
	if err != nil {
		// The task was deleted while its run was in flight. Nobody to tell,
		// and not a failure of the run.
		return
	}
	if task.CallbackURL == "" {
		return
	}
	s.enqueueCallback(ctx, run, task.CallbackURL)
}

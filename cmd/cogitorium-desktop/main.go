//go:build cgo

// Cogitorium as a desktop application.
//
// Requirement 13: one application, three shells. This is the second — the same
// server, the same embedded interface, in a native window instead of a browser
// tab. It is deliberately NOT a second application: it imports the same server
// package, serves the same bundle, and reads the same data directory, so there
// is nothing here that can drift out of step with the web shell.
//
// ## Why a separate binary
//
// A system webview needs cgo, and cgo cannot be cross-compiled from one machine
// to six targets. The server binary is pure Go precisely so it can be — that is
// what modernc.org/sqlite buys — so the desktop shell is its own command,
// built on a native runner per platform, and the server release is untouched.
//
// ## Why it binds port 0
//
// The window has to point at the server, so the server's address has to be
// known before the window opens. Asking for a fixed port would mean a second
// copy of the app, or anything else already holding 8688, deciding whether this
// one can start. The kernel picks a free port and the window is told which.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	webview "github.com/webview/webview_go"

	"github.com/orkcom-tech/cogitorium/internal/appstart"
	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/server"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/version"
)

func init() {
	// A window belongs to the main OS thread. On macOS anything else is a
	// crash rather than a warning, and the Go runtime is free to move a
	// goroutine between threads unless it is told not to.
	runtime.LockOSThread()
}

func main() {
	if err := run(); err != nil {
		// There is no terminal attached to a double-clicked application, so the
		// window is the only place an error can be seen. Failing silently is
		// the one outcome worth ruling out.
		slog.Error("cogitorium could not start", "err", err)
		showFailure(err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("", "")
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()})))
	slog.Info("starting cogitorium desktop", "data_dir", cfg.DataDir, "version", version.Version)

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The desktop shell is a local install by definition, so it listens on
	// loopback and the server treats the operator as the admin — the same rule
	// the web shell follows, not a special case for having a window.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("could not open a local port: %w", err)
	}
	cfg.Listen = ln.Addr().String()

	// The same startup decisions the server command makes, from the same place:
	// two of them refuse to start when a capability would be decorative or a
	// configured source unreadable, and a desktop build that quietly disagreed
	// with the server about that would be the worst possible place for the
	// disagreement to live.
	sb, err := appstart.SelectSandbox(ctx, cfg)
	if err != nil {
		return err
	}
	defer appstart.CloseSandbox(sb)
	searcher, err := appstart.BuildSearcher(cfg, sb)
	if err != nil {
		return err
	}
	env, err := appstart.BuildSecrets(ctx, cfg, db)
	if err != nil {
		return err
	}
	gate, err := appstart.BuildGearNet(cfg, db, sb)
	if err != nil {
		return err
	}
	defer gate.Close()

	srv := server.New(cfg, db, sb, searcher, env, gate)
	if err := srv.Bootstrap(ctx); err != nil {
		return fmt.Errorf("first-run setup: %w", err)
	}

	serveErr := make(chan error, 1)
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() { serveErr <- srv.Serve(serveCtx, ln) }()

	url := "http://" + ln.Addr().String()
	slog.Info("opening the window", "url", url)

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Cogitorium")
	// Large enough for the default arrangement — chat in the centre with the
	// blueprint beside it — rather than a size the operator has to fix first.
	w.SetSize(1440, 900, webview.HintNone)
	w.Navigate(url)
	w.Run() // returns when the window is closed

	// Closing the window ends the session: this is one application, and a
	// server still running with no window is a process the operator cannot see
	// and did not ask to keep.
	stopServing()
	if err := <-serveErr; err != nil && err.Error() != "http: Server closed" {
		return err
	}
	slog.Info("cogitorium desktop stopped")
	return nil
}

// showFailure puts a startup error in front of the operator.
//
// A double-clicked application has no terminal, so a message on stderr is a
// message nobody reads. The window is cheap and it is the only channel that
// exists at this point — an app that dies silently is indistinguishable from
// one that never launched.
func showFailure(err error) {
	defer func() { _ = recover() }() // a webview we cannot open must not mask the real error
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Cogitorium — could not start")
	w.SetSize(640, 320, webview.HintNone)
	w.SetHtml(`<!doctype html><meta charset="utf-8">
<style>
 body{margin:0;padding:28px;background:#0d0f11;color:#e7e8ea;
      font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}
 h1{font:600 15px/1.3 system-ui;letter-spacing:.02em;margin:0 0 12px;color:#1a8a69}
 pre{white-space:pre-wrap;overflow-wrap:anywhere;margin:0;color:#e7e8ea}
</style>
<h1>Cogitorium could not start</h1><pre>` + htmlEscape(err.Error()) + `</pre>`)
	w.Run()
}

// htmlEscape keeps an error message from being read as markup. The text comes
// from configuration and from the filesystem, so it is not ours to trust.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

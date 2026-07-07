// Command afweb runs the Phase-1 AgentFactory web console: a loopback HTTP server that serves
// the cyberpunk Floor view and drives the allowlisted af control verbs through the
// command-injection-safe exec wrapper. It binds 127.0.0.1:0 by default (ephemeral loopback).
//
// Usage:
//
//	cd <factory-root> && afweb
//
// The factory root is RESOLVED and VALIDATED at startup (AF_ROOT-first, else a walk-up from the
// current working directory to a real .agentfactory/factory.json); afweb fails loud when no factory
// resolves. AF_BIND may override the bind address; when it is not loopback, a session token is
// MANDATORY (printed at startup).
package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/stempeck/agentfactory-web/internal/config"
	"github.com/stempeck/agentfactory-web/internal/dispatch"
	"github.com/stempeck/agentfactory-web/internal/exec"
	"github.com/stempeck/agentfactory-web/internal/feedback"
	"github.com/stempeck/agentfactory-web/internal/formschema"
	"github.com/stempeck/agentfactory-web/internal/proto"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
	"github.com/stempeck/agentfactory-web/internal/rendezvous"
	"github.com/stempeck/agentfactory-web/internal/server"
	web "github.com/stempeck/agentfactory-web/internal/web"
)

func main() {
	// Resolve the VALIDATED factory root (#432) — AF_ROOT-first-then-cwd, walked up to a real
	// .agentfactory/factory.json — instead of the raw os.Getwd()/AF_ROOT value that could silently
	// serve a wrong-but-valid (or non-existent) factory. Validating here also fixes the ADR-010
	// rendezvous-file placement for free (the same validated string feeds rendezvous.Ensure below).
	// Fail loud when no factory resolves (mirrors the log.Fatalf precedents below): a bounded,
	// clearly-logged exit that the detached/bounded relaunch guard tolerates without busy-looping.
	root, err := config.ResolveFactoryRoot()
	if err != nil {
		log.Fatalf("afweb: cannot resolve factory root: %v", err)
	}

	// Wrapper.root (pre-flight) and ExecRunner.root (the af child's cwd) are sourced from the
	// SAME validated root above, so they can never diverge.
	runner := exec.NewExecRunner(root)
	wrapper := exec.NewWrapper(runner, root)
	rm := readmodel.New(wrapper, readmodel.NewTmuxLiveness())
	capture := readmodel.NewTmuxCapture() // #500: per-agent read-only session-snapshot reader (probe-first)
	forms := formschema.New(wrapper)
	disp := dispatch.New(wrapper)
	settings := config.New(root, wrapper)
	protos := proto.New(root)                // serve on-disk prototypes under <root>/.designs/
	feedbackWriter := feedback.New(root, rm) // gate-verify via the read-model (no new exec)

	opts := []server.Option{
		server.WithRoot(root), // surfaced via /healthz so a wrong-but-valid root is visible, not silent
		server.WithFormReader(forms),
		server.WithDispatchReader(disp),
		server.WithSettings(settings),
		server.WithFormulaResolver(settings), // #455: Sling form resolves the DECLARED formula from agents.json (same *config.Service)
		server.WithPrototypes(protos),
		server.WithFeedback(feedbackWriter),
		server.WithTailer(capture), // #500: GET /api/agents/{name}/detail session snapshot
		server.WithMailer(wrapper), // #500: POST /api/agents/{name}/mail (Wrapper.MailSend, sender=operator)
	}
	if bind := os.Getenv("AF_BIND"); bind != "" {
		opts = append(opts, server.WithBind(bind))
	}
	srv := server.New(wrapper, rm, web.Handler(), opts...)

	// Singleton-launch coordination (web-module rendezvous; re-implements ADR-010 by behavior, no
	// internal/ import). A second entrypoint launch that finds a live, healthy server no-ops rather
	// than binding a duplicate; otherwise we bind, publish .runtime/webui_server.json, and serve.
	var ln net.Listener
	url, owned, err := rendezvous.Ensure(root, fmt.Sprintf("webui-%d", os.Getpid()), func() (string, error) {
		l, lerr := srv.Listen()
		if lerr != nil {
			return "", lerr
		}
		ln = l
		return l.Addr().String(), nil
	})
	if err != nil {
		log.Fatalf("afweb: rendezvous failed: %v", err)
	}
	if !owned {
		// A healthy web UI is already running (it won the start-lock / published the endpoint).
		// Idempotent no-op so the IFF-available entrypoint guard can relaunch freely.
		log.Printf("afweb: web UI already running at %s; nothing to do", url)
		return
	}
	_ = ln                                      // the listener stays open; srv.Listen() serves on it in its own goroutine
	log.Printf("afweb: factory root: %s", root) // so an operator can see which factory this process resolved to
	log.Printf("afweb: serving the Floor at %s/", url)
	log.Printf("afweb: session token: %s  (required only when the bind is not loopback)", srv.Token())

	// Block forever; the server runs in its own goroutine.
	select {}
}

// Package server exposes pingcord's HTTP endpoints: the GitHub webhook, the
// Discord interactions endpoint and a health check.
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/benpsk/pingcord/internal/githubhook"
	"github.com/benpsk/pingcord/internal/notify"
)

const (
	// maxGitHubBody is GitHub's maximum webhook payload size.
	maxGitHubBody = 25 << 20
	// handleTimeout keeps us under GitHub's 10s delivery timeout.
	handleTimeout = 9 * time.Second
	// defaultMaxConcurrentWebhooks bounds memory used by unauthenticated
	// bodies being read before their signature can be checked.
	defaultMaxConcurrentWebhooks = 8
	// invalidSigLogEvery rate-limits warnings about bad signatures.
	invalidSigLogEvery = time.Minute
	maxLoggedHeaderLen = 64
)

// EventHandler processes a parsed GitHub event.
type EventHandler interface {
	Handle(ctx context.Context, ev githubhook.Event) (notify.Result, error)
}

// Options configures the HTTP handler.
type Options struct {
	WebhookSecret         []byte
	Org                   string // events from other orgs are ignored; empty accepts all
	Events                EventHandler
	Interactions          http.Handler // optional; /discord/interactions is 404 without it
	Logger                *slog.Logger
	MaxConcurrentWebhooks int // default 8
}

// NewHandler returns the root HTTP handler.
func NewHandler(opts Options) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	slots := opts.MaxConcurrentWebhooks
	if slots <= 0 {
		slots = defaultMaxConcurrentWebhooks
	}
	gh := &githubWebhook{
		secret: opts.WebhookSecret,
		org:    opts.Org,
		events: opts.Events,
		log:    log,
		slots:  make(chan struct{}, slots),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("POST /github/webhook", gh)
	if opts.Interactions != nil {
		mux.Handle("POST /discord/interactions", opts.Interactions)
	}
	return recoverer(log, mux)
}

// NewHTTPServer returns an http.Server with conservative timeouts.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

type githubWebhook struct {
	secret []byte
	org    string
	events EventHandler
	log    *slog.Logger
	slots  chan struct{}

	mu             sync.Mutex
	lastInvalidLog time.Time
	suppressed     int
}

func (g *githubWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "content type must be application/json (set it in the GitHub webhook settings)", http.StatusUnsupportedMediaType)
		return
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !githubhook.WellFormedSignature(sigHeader) {
		g.warnInvalidSignature(r)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "busy, retry later", http.StatusServiceUnavailable)
		return
	}

	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := githubhook.VerifySignature(g.secret, body, sigHeader); err != nil {
		g.warnInvalidSignature(r)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	g.handle(w, r, body)
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubBody))
	if err == nil {
		return body, true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
	} else {
		http.Error(w, "could not read body", http.StatusBadRequest)
	}
	return nil, false
}

// handle processes a delivery whose signature has been verified.
func (g *githubWebhook) handle(w http.ResponseWriter, r *http.Request, body []byte) {
	log := g.log.With("delivery", r.Header.Get("X-GitHub-Delivery"), "event", r.Header.Get("X-GitHub-Event"))
	name := r.Header.Get("X-GitHub-Event")
	switch name {
	case "":
		http.Error(w, "missing X-GitHub-Event header", http.StatusBadRequest)
		return
	case "ping":
		log.InfoContext(r.Context(), "github ping received")
		_, _ = io.WriteString(w, "pong\n")
		return
	}

	ev, err := githubhook.Parse(name, body)
	if err != nil {
		log.WarnContext(r.Context(), "malformed github payload", "error", err)
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	if g.org != "" && ev.Org != "" && !strings.EqualFold(ev.Org, g.org) {
		log.WarnContext(r.Context(), "ignoring event from another organization", "org", ev.Org)
		_, _ = io.WriteString(w, "ignored: other organization\n")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), handleTimeout)
	defer cancel()
	res, err := g.events.Handle(ctx, ev)
	if err != nil {
		log.ErrorContext(r.Context(), "notification failed", "action", ev.Action, "error", err)
		http.Error(w, "notification failed; redeliver from GitHub once resolved", http.StatusBadGateway)
		return
	}

	log.InfoContext(r.Context(), "github event handled",
		"action", ev.Action, "repo", ev.Repo, "pinged", len(res.Pinged), "unlinked", res.Unlinked)
	_, _ = io.WriteString(w, "ok\n")
}

// warnInvalidSignature logs bad-signature requests at most once per
// invalidSigLogEvery, with attacker-controlled values truncated, so an
// unauthenticated client can't flood the logs.
func (g *githubWebhook) warnInvalidSignature(r *http.Request) {
	g.mu.Lock()
	now := time.Now()
	if now.Sub(g.lastInvalidLog) < invalidSigLogEvery {
		g.suppressed++
		g.mu.Unlock()
		return
	}
	suppressed := g.suppressed
	g.lastInvalidLog, g.suppressed = now, 0
	g.mu.Unlock()

	g.log.WarnContext(r.Context(), "rejected webhook with invalid signature",
		"client", clientIP(r),
		"delivery", truncate(r.Header.Get("X-GitHub-Delivery")),
		"event", truncate(r.Header.Get("X-GitHub-Event")),
		"suppressed_since_last", suppressed)
}

// clientIP prefers the address added by the reverse proxy (Caddy).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return truncate(strings.TrimSpace(parts[len(parts)-1]))
	}
	return r.RemoteAddr
}

func truncate(s string) string {
	if len(s) > maxLoggedHeaderLen {
		return s[:maxLoggedHeaderLen] + "…"
	}
	return s
}

// recoverer turns panics into 500s without leaking details.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic serving request", "path", r.URL.Path, "panic", rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

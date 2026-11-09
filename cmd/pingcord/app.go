package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/benpsk/pingcord/internal/config"
	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/notify"
	"github.com/benpsk/pingcord/internal/server"
	"github.com/benpsk/pingcord/internal/store"
)

const (
	shutdownTimeout     = 10 * time.Second
	startupCheckTimeout = 15 * time.Second
	tokenExpiryWarning  = 14 * 24 * time.Hour
)

// serve wires everything together and runs the HTTP server until ctx ends.
func serve(ctx context.Context, getenv func(string) string, logOut io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: cfg.LogLevel}))

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	gh := githubapi.NewClient(cfg.GitHub.APIBaseURL, cfg.GitHub.Token, cfg.GitHub.Org, nil)
	dir := githubapi.NewDirectory(gh, st, githubapi.WithLogger(log))
	sender := discord.NewWebhookClient(cfg.Discord.WebhookURL, nil)
	notifier := notify.New(st, dir, sender, notify.WithLogger(log), notify.WithIgnoreBots(cfg.IgnoreBots))

	go checkGitHubAccess(ctx, gh, log)

	handler := server.NewHandler(server.Options{
		WebhookSecret: cfg.GitHub.WebhookSecret,
		Events:        notifier,
		Logger:        log,
	})
	return listen(ctx, server.NewHTTPServer(cfg.Addr, handler), log)
}

func listen(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		log.Info("pingcord listening", "addr", srv.Addr, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// checkGitHubAccess verifies the token at startup and warns before it expires.
func checkGitHubAccess(ctx context.Context, gh *githubapi.Client, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, startupCheckTimeout)
	defer cancel()

	expiry, err := gh.CheckAccess(ctx)
	switch {
	case err != nil:
		log.Error("GITHUB_TOKEN cannot read org members; @mentions and team pings will not resolve",
			"org", gh.Org(), "error", err)
	case expiry.IsZero():
		log.Info("github token ok", "org", gh.Org())
	case time.Until(expiry) < tokenExpiryWarning:
		log.Warn("GITHUB_TOKEN expires soon; rotate it", "expires", expiry)
	default:
		log.Info("github token ok", "org", gh.Org(), "expires", expiry)
	}
}

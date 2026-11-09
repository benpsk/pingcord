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
	"github.com/benpsk/pingcord/internal/discordcmd"
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

	interactions := discordcmd.NewHandler(discordcmd.Config{
		PublicKey: cfg.Discord.PublicKey,
		GuildID:   cfg.Discord.GuildID,
		Org:       cfg.GitHub.Org,
		IsAdmin:   cfg.IsAdmin,
	}, st, dir, sender, discordcmd.WithLogger(log))

	go checkGitHubAccess(ctx, gh, dir, log)
	go registerCommands(ctx, cfg.Discord, log)

	handler := server.NewHandler(server.Options{
		WebhookSecret: cfg.GitHub.WebhookSecret,
		Org:           cfg.GitHub.Org,
		Events:        notifier,
		Interactions:  interactions,
		Logger:        log,
	})
	err = listen(ctx, server.NewHTTPServer(cfg.Addr, handler), log)
	interactions.Wait() // flush in-flight audit announcements
	return err
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

// checkGitHubAccess verifies the token at startup, warns before it expires,
// and warms the org member and repo caches used by /admin autocomplete.
func checkGitHubAccess(ctx context.Context, gh *githubapi.Client, dir *githubapi.Directory, log *slog.Logger) {
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
	if err == nil {
		if _, err := dir.OrgMembers(ctx); err != nil {
			log.Warn("could not preload org members", "error", err)
		}
		// Only public repos are visible without the token's repository
		// access, so the count shows whether private ones are included.
		if repos, err := dir.OrgRepos(ctx); err != nil {
			log.Warn("could not list org repos; /admin watch suggests only repos seen in webhooks", "error", err)
		} else {
			log.Info("github repos visible", "count", len(repos))
		}
	}
}

// registerCommands installs /admin in the guild. Failure is logged, not
// fatal: GitHub notifications keep working without the admin commands.
func registerCommands(ctx context.Context, d config.Discord, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, startupCheckTimeout)
	defer cancel()

	if err := discordcmd.RegisterCommands(ctx, nil, d.APIBaseURL, d.AppID, d.GuildID, d.BotToken); err != nil {
		log.Error("could not register /admin slash commands; check DISCORD_APP_ID, DISCORD_GUILD_ID, DISCORD_BOT_TOKEN and that the app is in the server", "error", err)
		return
	}
	log.Info("registered /admin slash commands", "guild", d.GuildID)
}

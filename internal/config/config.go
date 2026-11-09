// Package config loads and validates pingcord's configuration from the
// environment. All required values are checked at startup and every problem
// is reported at once. Secret values never appear in error messages.
package config

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultPort          = "8080"
	defaultDBPath        = "/data/pingcord.db"
	minWebhookSecretLen  = 16
	defaultGitHubAPIURL  = "https://api.github.com"
	defaultDiscordAPIURL = "https://discord.com/api/v10"
)

var (
	snowflakeRe = regexp.MustCompile(`^[0-9]{17,20}$`)
	orgRe       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
)

// Config is the fully validated runtime configuration.
type Config struct {
	Addr            string
	DBPath          string
	LogLevel        slog.Level
	IgnoreBots      bool
	AdminDiscordIDs []string
	GitHub          GitHub
	Discord         Discord
}

// GitHub holds GitHub-side settings.
type GitHub struct {
	WebhookSecret []byte
	Token         string
	Org           string
	APIBaseURL    string
}

// Discord holds Discord-side settings.
type Discord struct {
	WebhookURL string
	AppID      string
	PublicKey  ed25519.PublicKey
	BotToken   string
	GuildID    string
	APIBaseURL string
}

// IsAdmin reports whether the Discord user ID is a configured admin.
func (c Config) IsAdmin(discordID string) bool {
	return slices.Contains(c.AdminDiscordIDs, discordID)
}

// Load reads configuration using getenv (normally os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}

	cfg := Config{
		Addr:            ":" + l.port("PORT"),
		DBPath:          l.optional("DB_PATH", defaultDBPath),
		LogLevel:        l.logLevel("LOG_LEVEL"),
		IgnoreBots:      l.boolean("IGNORE_BOTS", true),
		AdminDiscordIDs: l.snowflakeList("ADMIN_DISCORD_IDS"),
		GitHub: GitHub{
			WebhookSecret: []byte(l.secret("GITHUB_WEBHOOK_SECRET", minWebhookSecretLen)),
			Token:         l.required("GITHUB_TOKEN"),
			Org:           l.matching("GITHUB_ORG", orgRe, "must be a valid GitHub organization login"),
			APIBaseURL:    defaultGitHubAPIURL,
		},
		Discord: Discord{
			WebhookURL: l.discordWebhookURL("DISCORD_WEBHOOK_URL"),
			AppID:      l.matching("DISCORD_APP_ID", snowflakeRe, "must be a Discord ID (17-20 digits)"),
			PublicKey:  l.publicKey("DISCORD_PUBLIC_KEY"),
			BotToken:   l.required("DISCORD_BOT_TOKEN"),
			GuildID:    l.matching("DISCORD_GUILD_ID", snowflakeRe, "must be a Discord ID (17-20 digits)"),
			APIBaseURL: defaultDiscordAPIURL,
		},
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// loader accumulates validation errors so all of them are reported together.
type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) fail(key, msg string) {
	l.errs = append(l.errs, fmt.Errorf("  %s %s", key, msg))
}

func (l *loader) value(key string) string {
	return strings.TrimSpace(l.getenv(key))
}

func (l *loader) required(key string) string {
	v := l.value(key)
	if v == "" {
		l.fail(key, "is required")
	}
	return v
}

func (l *loader) optional(key, def string) string {
	if v := l.value(key); v != "" {
		return v
	}
	return def
}

func (l *loader) secret(key string, minLen int) string {
	v := l.required(key)
	if v != "" && len(v) < minLen {
		l.fail(key, fmt.Sprintf("must be at least %d characters", minLen))
	}
	return v
}

func (l *loader) matching(key string, re *regexp.Regexp, msg string) string {
	v := l.required(key)
	if v != "" && !re.MatchString(v) {
		l.fail(key, msg)
	}
	return v
}

func (l *loader) port(key string) string {
	v := l.optional(key, defaultPort)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		l.fail(key, "must be a number between 1 and 65535")
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	v := l.value(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be true or false")
		return def
	}
	return b
}

func (l *loader) logLevel(key string) slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(l.optional(key, "info"))); err != nil {
		l.fail(key, "must be one of debug, info, warn, error")
		return slog.LevelInfo
	}
	return lvl
}

func (l *loader) snowflakeList(key string) []string {
	raw := l.required(key)
	if raw == "" {
		return nil
	}
	var ids []string
	for part := range strings.SplitSeq(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if !snowflakeRe.MatchString(id) {
			l.fail(key, fmt.Sprintf("contains an invalid Discord ID %q", id))
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		l.fail(key, "must contain at least one Discord user ID")
	}
	return ids
}

// discordWebhookURL validates the URL without echoing it: it contains a token.
func (l *loader) discordWebhookURL(key string) string {
	v := l.required(key)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	validHost := err == nil && (u.Host == "discord.com" || u.Host == "discordapp.com" ||
		u.Host == "canary.discord.com" || u.Host == "ptb.discord.com")
	if !validHost || u.Scheme != "https" || !strings.HasPrefix(u.Path, "/api/webhooks/") {
		l.fail(key, "must be an https://discord.com/api/webhooks/... URL")
	}
	return v
}

func (l *loader) publicKey(key string) ed25519.PublicKey {
	v := l.required(key)
	if v == "" {
		return nil
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != ed25519.PublicKeySize {
		l.fail(key, "must be the 64-character hex public key from the Discord developer portal")
		return nil
	}
	return ed25519.PublicKey(b)
}

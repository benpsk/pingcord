package config

import (
	"encoding/hex"
	"log/slog"
	"maps"
	"strings"
	"testing"
)

const testPublicKey = "e7a3c1d0f2b4a6c8e0f1a3b5c7d9e1f3a5b7c9d1e3f5a7b9c1d3e5f7a9b1c3d5"

func validEnv() map[string]string {
	return map[string]string{
		"GITHUB_WEBHOOK_SECRET": "s3cret-s3cret-s3cret",
		"GITHUB_TOKEN":          "github_pat_xxx",
		"GITHUB_ORG":            "my-org",
		"DISCORD_WEBHOOK_URL":   "https://discord.com/api/webhooks/123456789012345678/abcDEF-123_xyz",
		"DISCORD_APP_ID":        "123456789012345678",
		"DISCORD_PUBLIC_KEY":    testPublicKey,
		"DISCORD_BOT_TOKEN":     "bot-token",
		"DISCORD_GUILD_ID":      "223456789012345678",
		"ADMIN_DISCORD_IDS":     "323456789012345678, 423456789012345678",
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.DBPath != "/data/pingcord.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if !cfg.IgnoreBots {
		t.Error("IgnoreBots should default to true")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if string(cfg.GitHub.WebhookSecret) != "s3cret-s3cret-s3cret" || cfg.GitHub.Org != "my-org" {
		t.Errorf("GitHub = %+v", cfg.GitHub)
	}
	if want, _ := hex.DecodeString(testPublicKey); string(cfg.Discord.PublicKey) != string(want) {
		t.Error("PublicKey not decoded")
	}
	if cfg.Discord.APIBaseURL != "https://discord.com/api/v10" || cfg.GitHub.APIBaseURL != "https://api.github.com" {
		t.Errorf("API base URLs = %q / %q", cfg.Discord.APIBaseURL, cfg.GitHub.APIBaseURL)
	}
	if !cfg.IsAdmin("423456789012345678") || cfg.IsAdmin("999999999999999999") {
		t.Errorf("admin IDs = %v", cfg.AdminDiscordIDs)
	}
}

func TestLoadOptionalOverrides(t *testing.T) {
	env := validEnv()
	env["PORT"] = "9000"
	env["DB_PATH"] = "/tmp/x.db"
	env["IGNORE_BOTS"] = "false"
	env["LOG_LEVEL"] = "debug"

	cfg, err := Load(getenvFrom(env))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Addr != ":9000" || cfg.DBPath != "/tmp/x.db" || cfg.IgnoreBots || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(env map[string]string)
		wantErr []string
	}{
		{
			name:    "all required missing are reported together",
			mutate:  func(env map[string]string) { clear(env) },
			wantErr: []string{"GITHUB_WEBHOOK_SECRET", "GITHUB_TOKEN", "GITHUB_ORG", "DISCORD_WEBHOOK_URL", "DISCORD_APP_ID", "DISCORD_PUBLIC_KEY", "DISCORD_BOT_TOKEN", "DISCORD_GUILD_ID", "ADMIN_DISCORD_IDS"},
		},
		{name: "short webhook secret", mutate: func(e map[string]string) { e["GITHUB_WEBHOOK_SECRET"] = "short" }, wantErr: []string{"GITHUB_WEBHOOK_SECRET", "at least 16"}},
		{name: "invalid org", mutate: func(e map[string]string) { e["GITHUB_ORG"] = "bad org!" }, wantErr: []string{"GITHUB_ORG"}},
		{name: "webhook URL not discord", mutate: func(e map[string]string) { e["DISCORD_WEBHOOK_URL"] = "https://evil.example/api/webhooks/1/x" }, wantErr: []string{"DISCORD_WEBHOOK_URL"}},
		{name: "webhook URL http", mutate: func(e map[string]string) { e["DISCORD_WEBHOOK_URL"] = "http://discord.com/api/webhooks/1/x" }, wantErr: []string{"DISCORD_WEBHOOK_URL"}},
		{name: "bad app id", mutate: func(e map[string]string) { e["DISCORD_APP_ID"] = "abc" }, wantErr: []string{"DISCORD_APP_ID"}},
		{name: "bad guild id", mutate: func(e map[string]string) { e["DISCORD_GUILD_ID"] = "12" }, wantErr: []string{"DISCORD_GUILD_ID"}},
		{name: "bad public key hex", mutate: func(e map[string]string) { e["DISCORD_PUBLIC_KEY"] = "zz" }, wantErr: []string{"DISCORD_PUBLIC_KEY"}},
		{name: "bad public key length", mutate: func(e map[string]string) { e["DISCORD_PUBLIC_KEY"] = "abcd" }, wantErr: []string{"DISCORD_PUBLIC_KEY"}},
		{name: "bad admin id", mutate: func(e map[string]string) { e["ADMIN_DISCORD_IDS"] = "323456789012345678,nope" }, wantErr: []string{"ADMIN_DISCORD_IDS", "nope"}},
		{name: "empty admin list", mutate: func(e map[string]string) { e["ADMIN_DISCORD_IDS"] = " , " }, wantErr: []string{"ADMIN_DISCORD_IDS"}},
		{name: "bad port", mutate: func(e map[string]string) { e["PORT"] = "70000" }, wantErr: []string{"PORT"}},
		{name: "non-numeric port", mutate: func(e map[string]string) { e["PORT"] = "http" }, wantErr: []string{"PORT"}},
		{name: "bad bool", mutate: func(e map[string]string) { e["IGNORE_BOTS"] = "maybe" }, wantErr: []string{"IGNORE_BOTS"}},
		{name: "bad log level", mutate: func(e map[string]string) { e["LOG_LEVEL"] = "loud" }, wantErr: []string{"LOG_LEVEL"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := maps.Clone(validEnv())
			tt.mutate(env)

			_, err := Load(getenvFrom(env))

			if err == nil {
				t.Fatal("Load() expected error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestLoadErrorsNeverLeakSecrets(t *testing.T) {
	env := validEnv()
	env["DISCORD_WEBHOOK_URL"] = "https://evil.example/api/webhooks/1/super-secret-token"
	env["GITHUB_WEBHOOK_SECRET"] = "tiny"

	_, err := Load(getenvFrom(env))

	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"super-secret-token", "tiny"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks secret %q: %v", secret, err)
		}
	}
}

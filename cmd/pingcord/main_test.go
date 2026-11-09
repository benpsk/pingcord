package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func envFunc(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestRunVersionAndFlags(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantStdout string
	}{
		{name: "version flag prints version", args: []string{"-version"}, wantStdout: "dev\n"},
		{name: "unknown flag returns an error", args: []string{"-nope"}, wantErr: "flag provided but not defined"},
		{name: "missing configuration is reported", args: nil, wantErr: "GITHUB_WEBHOOK_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			err := run(context.Background(), tt.args, envFunc(nil), &stdout, &stderr)

			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if stdout.String() != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
		})
	}
}

func TestRunHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	var out bytes.Buffer

	if err := run(context.Background(), []string{"-healthcheck"}, envFunc(map[string]string{"PORT": port}), &out, &out); err != nil {
		t.Fatalf("healthy server: %v", err)
	}

	srv.Close()
	if err := run(context.Background(), []string{"-healthcheck"}, envFunc(map[string]string{"PORT": port}), &out, &out); err == nil {
		t.Fatal("closed server should fail the healthcheck")
	}
}

// TestRunServesUntilCancelled boots the real wiring against fake GitHub and
// Discord APIs, delivers a signed webhook and shuts down cleanly.
func TestRunServesUntilCancelled(t *testing.T) {
	var (
		mu       sync.Mutex
		ghPaths  []string
		cmdCalls int
	)
	fakeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/gh/"):
			ghPaths = append(ghPaths, r.URL.Path)
			if strings.HasPrefix(r.URL.Path, "/gh/users/") {
				fmt.Fprint(w, `{"id":3,"login":"carol"}`)
				return
			}
			fmt.Fprint(w, `[]`)
		case strings.HasPrefix(r.URL.Path, "/discord/applications/"):
			cmdCalls++
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeAPI.Close()

	port := freePort(t)
	secret := "e2e-secret-e2e-secret"
	env := map[string]string{
		"PORT":                  port,
		"DB_PATH":               filepath.Join(t.TempDir(), "pingcord.db"),
		"GITHUB_WEBHOOK_SECRET": secret,
		"GITHUB_TOKEN":          "tok",
		"GITHUB_ORG":            "my-org",
		"GITHUB_API_URL":        fakeAPI.URL + "/gh",
		"DISCORD_WEBHOOK_URL":   "https://discord.com/api/webhooks/1/token",
		"DISCORD_API_URL":       fakeAPI.URL + "/discord",
		"DISCORD_APP_ID":        "123456789012345678",
		"DISCORD_PUBLIC_KEY":    strings.Repeat("ab", 32),
		"DISCORD_BOT_TOKEN":     "bot",
		"DISCORD_GUILD_ID":      "223456789012345678",
		"ADMIN_DISCORD_IDS":     "323456789012345678",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var logs syncBuffer
	go func() { done <- run(ctx, nil, envFunc(env), &logs, &logs) }()

	base := "http://127.0.0.1:" + port
	waitHealthy(t, base)

	body := []byte(`{"action":"created","issue":{"number":1,"title":"t","user":{"id":1,"login":"alice"}},"comment":{"body":"hi @carol","user":{"id":2,"login":"bob"}},"sender":{"id":2,"login":"bob"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/github/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d; logs:\n%s", resp.StatusCode, logs.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v; logs:\n%s", err, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down")
	}

	mu.Lock()
	defer mu.Unlock()
	if !containsPrefix(ghPaths, "/gh/users/carol") {
		t.Errorf("mention was not resolved via GitHub API; paths = %v", ghPaths)
	}
	if !strings.Contains(logs.String(), `"msg":"github event handled"`) {
		t.Errorf("expected JSON log line for handled event; logs:\n%s", logs.String())
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func containsPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

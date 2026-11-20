package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise the complete Telegram-only wiring: private account linking, watcher
// setup, signed GitHub delivery, private recipient delivery and graceful exit.
func TestTelegramServeDeliversGitHubNotifications(t *testing.T) {
	var mu sync.Mutex
	var notifications = map[string][]string{}
	fakeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/gh/users/"):
			if strings.HasSuffix(r.URL.Path, "/carol") {
				_, _ = io.WriteString(w, `{"id":43,"login":"carol"}`)
			} else {
				_, _ = io.WriteString(w, `{"id":42,"login":"alice"}`)
			}
		case strings.HasPrefix(r.URL.Path, "/gh/"):
			_, _ = io.WriteString(w, `[]`)
		case r.URL.Path == "/bot123:secret/sendMessage":
			var m struct {
				ChatID string `json:"chat_id"`
				Text   string `json:"text"`
				Silent bool   `json:"disable_notification"`
			}
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Error(err)
			}
			if m.ChatID != "123" && !m.Silent {
				mu.Lock()
				notifications[m.ChatID] = append(notifications[m.ChatID], m.Text)
				mu.Unlock()
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			t.Error("unexpected API request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeAPI.Close()
	port := freePort(t)
	secret := "github-webhook-secret"
	telegramSecret := "telegram-webhook-secret"
	env := map[string]string{
		"PORT": port, "DB_PATH": filepath.Join(t.TempDir(), "telegram.db"),
		"GITHUB_WEBHOOK_SECRET": secret, "GITHUB_TOKEN": "token", "GITHUB_ORG": "acme", "GITHUB_API_URL": fakeAPI.URL + "/gh",
		"NOTIFICATION_PROVIDER": "telegram", "TELEGRAM_BOT_TOKEN": "123:secret",
		"TELEGRAM_API_URL": fakeAPI.URL, "TELEGRAM_WEBHOOK_SECRET": telegramSecret, "ADMIN_TELEGRAM_IDS": "123",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var logs syncBuffer
	go func() { done <- serve(ctx, envFunc(env), &logs) }()
	base := "http://127.0.0.1:" + port
	waitHealthy(t, base)
	post := func(path string, body []byte, headers map[string]string, want int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: status %d: %s; logs %s", path, resp.StatusCode, b, logs.String())
		}
	}
	for i, command := range []string{"/admin link 456 alice", "/admin watch 456 all api", "/admin link 789 carol", "/admin watch 789 new-prs api"} {
		body := []byte(fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"from":{"id":123},"chat":{"id":123,"type":"private"}}}`, i+1, command))
		post("/telegram/webhook", body, map[string]string{"X-Telegram-Bot-Api-Secret-Token": telegramSecret}, 200)
	}
	body := []byte(`{"action":"opened","organization":{"login":"acme"},"repository":{"full_name":"acme/api","owner":{"login":"acme"}},"pull_request":{"number":1,"title":"Add login","html_url":"https://github.com/acme/api/pull/1","user":{"id":2,"login":"bob"}},"sender":{"id":2,"login":"bob"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	headers := map[string]string{"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
	post("/github/webhook", body, headers, 200)
	mu.Lock()
	if len(notifications) != 2 {
		t.Errorf("unexpected recipients: %v", notifications)
	}
	for _, id := range []string{"456", "789"} {
		messages := notifications[id]
		if len(messages) != 1 || !strings.Contains(messages[0], "Add login") || !strings.Contains(messages[0], "You: new PR") || strings.Contains(messages[0], "tg://user") {
			t.Errorf("private notification for %s: %v", id, messages)
		}
	}
	mu.Unlock()
	post("/discord/interactions", []byte(`{}`), nil, 404)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/discordcmd"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/store"
)

func TestRenderEscapesTextWithoutMentionLinks(t *testing.T) {
	msg := discord.Message{Content: "<@123> review requested\n<@456> other @everyone", AllowedMentions: discord.AllowedMentions{Users: []string{"123"}}, Embeds: []discord.Embed{{Title: `Fix <script> & bugs`, Description: `<a href="tg://user?id=456">attack</a> @someone`, URL: "https://github.com/acme/api/pull/1"}}}
	got := Render(msg)
	if strings.Contains(got, `href="tg://user?id=`) || strings.Contains(got, "<script>") || strings.Contains(got, "@everyone") || strings.Contains(got, `\n`) {
		t.Fatalf("unsafe or malformed rendering: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "Open on GitHub") {
		t.Fatal(got)
	}
	msg.Embeds[0].URL = "javascript:alert(1)"
	if strings.Contains(Render(msg), "javascript:") {
		t.Fatal("unsafe URL")
	}
}

func TestClientChecksAPIAndHidesToken(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		fail       bool
	}{
		{"success", `{"ok":true}`, 200, false},
		{"api rejection", `{"ok":false,"description":"secret-token"}`, 200, true},
		{"http failure", `{"ok":true}`, 500, true},
		{"malformed", `oops`, 200, true},
		{"long rate limit", `{"ok":false,"parameters":{"retry_after":60}}`, 429, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bot123:secret-token/sendMessage" {
					t.Error(r.URL.Path)
				}
				var v map[string]any
				if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
					t.Error(err)
				}
				if v["chat_id"] != "123" || v["parse_mode"] != "HTML" || v["disable_notification"] != true {
					t.Error(v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			err := NewClient(srv.URL, "123:secret-token", []string{"123"}, nil).Send(context.Background(), discord.Message{Content: "audit"})
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Fatal("token leaked")
			}
		})
	}
	c := NewClient("http://127.0.0.1:1", "123:secret-token", nil, nil)
	if err := c.Reply(context.Background(), "1", "test"); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
}

type directory struct{}

func (directory) LookupUser(context.Context, string) (githubapi.User, error) {
	return githubapi.User{ID: 42, Login: "alice"}, nil
}
func (directory) OrgMembers(context.Context) ([]githubapi.User, error) {
	return []githubapi.User{{ID: 42, Login: "alice"}}, nil
}
func (directory) OrgRepos(context.Context) ([]string, error) { return []string{"api"}, nil }

func TestWebhookAdminCommandsAndAuthorization(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var messages []map[string]any
	var messageMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		messageMu.Lock()
		messages = append(messages, payload)
		messageMu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(srv.URL, "1:secret", []string{"123"}, nil)
	commands := discordcmd.NewHandler(discordcmd.Config{Org: "acme", IsAdmin: func(id string) bool { return id == "123" }}, st, directory{}, client)
	defer commands.Wait()
	h := NewHandler("webhook-secret", client, commands, log)
	send := func(id int, caller int, chatType, secret, text string) int {
		t.Helper()
		chat := caller
		if chatType != "private" {
			chat = -100
		}
		payload := map[string]any{"update_id": id, "message": map[string]any{"text": text, "from": map[string]any{"id": caller}, "chat": map[string]any{"id": chat, "type": chatType}}}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/telegram/webhook", strings.NewReader(string(b)))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if got := send(1, 123, "private", "wrong", "/admin link 123 alice"); got != 401 {
		t.Fatal(got)
	}
	send(2, 999, "private", "webhook-secret", "/admin link 123 alice")
	send(3, 123, "group", "webhook-secret", "/admin link 123 alice")
	links, _ := st.ListLinks(ctx)
	if len(links) != 0 {
		t.Fatal("unauthorized mutation")
	}
	send(4, 123, "private", "webhook-secret", "/admin link 123 alice")
	send(4, 123, "private", "webhook-secret", "/admin unlink 123") // duplicate update ignored
	links, _ = st.ListLinks(ctx)
	if len(links) != 1 || links[0].GitHubID != 42 {
		t.Fatal(links)
	}
	send(5, 123, "private", "webhook-secret", "/admin watch 123 all acme/api")
	watches, _ := st.WatchersFor(ctx, "api")
	if len(watches) != 1 || watches[0].Level != "all" {
		t.Fatal(watches)
	}
	send(6, 123, "private", "webhook-secret", "/admin ignore alice")
	ignored, _ := st.IgnoredIDs(ctx, []int64{42})
	if !ignored[42] {
		t.Fatal("ignore missing")
	}
	send(7, 123, "private", "webhook-secret", "/admin unignore alice")
	send(8, 123, "private", "webhook-secret", "/admin unwatch 123 api")
	watches, _ = st.WatchersFor(ctx, "api")
	if len(watches) != 0 {
		t.Fatal(watches)
	}
	send(9, 123, "private", "webhook-secret", "/admin list")
	send(10, 123, "private", "webhook-secret", "/admin unlinked")
	send(11, 123, "private", "webhook-secret", "/whoami")
	send(12, 123, "private", "webhook-secret", "/admin unlink 123")
	links, _ = st.ListLinks(ctx)
	if len(links) != 0 {
		t.Fatal(links)
	}
	commands.Wait()
	messageMu.Lock()
	defer messageMu.Unlock()
	for _, m := range messages {
		if m["disable_notification"] == true && m["chat_id"] != "123" {
			t.Fatal("audit leaked to non-admin", m)
		}
		if m["chat_id"] != "123" && m["chat_id"] != "999" {
			t.Fatal("reply leaked", m)
		}
	}
}

func TestParseRejectsInvalidUserIDsAndArguments(t *testing.T) {
	for _, args := range [][]string{{"link", "-1", "alice"}, {"link", "@alice", "alice"}, {"watch", "1", "all"}, {"unknown"}, {"list", "extra"}, {}} {
		if _, _, ok := parseAdmin(args); ok {
			t.Fatal(args)
		}
	}
}

func TestClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewClient("http://localhost", "1:secret", nil, nil).Reply(ctx, "1", "hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRenderFitsTelegramLimitWithEmojiAndMentions(t *testing.T) {
	for _, content := range []string{strings.Repeat("😀", 2000), strings.Repeat("@", 2000), strings.Repeat("<@123> reason\n", 100)} {
		msg := discord.Message{Content: content, AllowedMentions: discord.AllowedMentions{Users: []string{"123"}}, Embeds: []discord.Embed{{Title: strings.Repeat("😀", 256), Description: strings.Repeat("@", 300), Author: &discord.EmbedAuthor{Name: strings.Repeat("😀", 100)}, Footer: &discord.EmbedFooter{Text: strings.Repeat("😀", 200)}, URL: "https://github.com/acme/api"}}}
		rendered := Render(msg)
		plain := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(rendered, ""))
		if textUnits(plain) > 4096 {
			t.Fatalf("rendered text has %d units", textUnits(plain))
		}
	}
	if truncateUnits("😀a", 3) != "😀a" || truncateUnits("😀ab", 3) != "😀…" {
		t.Fatal("incorrect UTF16 truncation")
	}
}

func TestDirectNotificationAndAdminAuditsStayPrivate(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		requests = append(requests, payload)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:secret", []string{"123", "456", "123"}, nil)
	msg := discord.Message{Content: "<@789> review requested", Embeds: []discord.Embed{{Title: "Fix login"}}}
	if err := c.SendDirect(context.Background(), msg, "789"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0]["chat_id"] != "789" || requests[0]["disable_notification"] != false || !strings.HasPrefix(requests[0]["text"].(string), "You: review requested") {
		t.Fatal(requests)
	}
	if err := c.Send(context.Background(), msg); err == nil {
		t.Fatal("notification without explicit recipient accepted")
	}
	for _, id := range []string{"-100", "0", "@group", "+123", "00123"} {
		if err := c.SendDirect(context.Background(), msg, id); err == nil {
			t.Fatal("non-private ID accepted", id)
		}
	}
	if err := c.Send(context.Background(), discord.Message{Content: "audit <@789>"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatal("unexpected fanout", requests)
	}
	for i, id := range []string{"123", "456"} {
		req := requests[i+1]
		if req["chat_id"] != id || req["disable_notification"] != true || strings.Contains(req["text"].(string), "tg://user") {
			t.Fatal("invalid audit delivery", req)
		}
	}
}

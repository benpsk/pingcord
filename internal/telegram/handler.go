package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Commands is the shared, allowlist-protected admin command service.
type Commands interface {
	Execute(context.Context, string, string, map[string]string) string
}

// Handler authenticates Telegram webhooks and handles private admin commands.
type Handler struct {
	secret   string
	client   *Client
	commands Commands
	log      *slog.Logger
	// Serialize command updates and remember recent successes so Telegram retries
	// do not repeat mutations and audit messages within this process.
	mu        sync.Mutex
	completed map[int64]string
	order     []int64
	slots     chan struct{}
}

// NewHandler returns a bot webhook handler using a shared admin service.
func NewHandler(secret string, c *Client, commands Commands, log *slog.Logger) *Handler {
	return &Handler{secret: secret, client: c, commands: commands, log: log, completed: map[int64]string{}, slots: make(chan struct{}, 8)}
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		From *struct {
			ID    int64 `json:"id"`
			IsBot bool  `json:"is_bot"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

const help = `Admin commands (private chat only):
/admin link TELEGRAM_USER_ID GITHUB_LOGIN
/admin unlink TELEGRAM_USER_ID
/admin list
/admin unlinked
/admin ignore GITHUB_LOGIN
/admin unignore GITHUB_LOGIN
/admin watch TELEGRAM_USER_ID new-prs|prs|all REPO_OR_*
/admin unwatch TELEGRAM_USER_ID REPO_OR_*
/whoami — show your user ID and this chat ID
Start this bot before an admin links you. GitHub notifications arrive here privately.`

// ServeHTTP accepts secret-authenticated Telegram message updates.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(h.secret)) != 1 {
		http.Error(w, "invalid webhook secret", http.StatusUnauthorized)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	var in update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	m := in.Message
	if m == nil || m.From == nil || m.From.IsBot {
		w.WriteHeader(http.StatusOK)
		return
	}
	args := strings.Fields(m.Text)
	if len(args) == 0 {
		return
	}
	cmd, _, _ := strings.Cut(args[0], "@")
	if cmd != "/start" && cmd != "/help" && cmd != "/whoami" && cmd != "/admin" {
		return
	}
	// Commands are private-only, including help and whoami.
	if m.Chat.Type != "private" || m.Chat.ID <= 0 || m.Chat.ID != m.From.ID {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if reply, ok := h.completed[in.ID]; ok {
		if err := h.client.Reply(ctx, strconv.FormatInt(m.Chat.ID, 10), reply); err != nil {
			http.Error(w, "reply failed", http.StatusBadGateway)
		}
		return
	}
	caller := strconv.FormatInt(m.From.ID, 10)
	reply := help
	switch cmd {
	case "/whoami", "/start":
		reply = fmt.Sprintf("Your Telegram user ID: %s\nChat ID: %d\n\n%s", caller, m.Chat.ID, help)
	case "/admin":
		sub, values, ok := parseAdmin(args[1:])
		if ok {
			reply = h.commands.Execute(ctx, caller, sub, values)
		}
	}
	// Cache before replying so a retry can resend the result without repeating
	// the mutation. A restart may still replay an update.
	h.completed[in.ID] = reply
	h.order = append(h.order, in.ID)
	if len(h.order) > 1024 {
		delete(h.completed, h.order[0])
		h.order = h.order[1:]
	}
	if err := h.client.Reply(ctx, strconv.FormatInt(m.Chat.ID, 10), reply); err != nil {
		h.log.ErrorContext(ctx, "telegram command reply failed", "error", err)
		http.Error(w, "reply failed", http.StatusBadGateway)
	}
}

func parseAdmin(args []string) (string, map[string]string, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	fields := map[string][]string{
		"link": {"user", "github"}, "unlink": {"user"}, "list": {}, "unlinked": {},
		"ignore": {"github"}, "unignore": {"github"}, "watch": {"user", "level", "repo"}, "unwatch": {"user", "repo"},
	}
	names, ok := fields[args[0]]
	if !ok || len(args) != len(names)+1 {
		return "", nil, false
	}
	values := map[string]string{}
	for i, name := range names {
		value := args[i+1]
		if name == "user" {
			if id, err := strconv.ParseInt(value, 10, 64); err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
				return "", nil, false
			}
		}
		values[name] = value
	}
	return args[0], values, true
}

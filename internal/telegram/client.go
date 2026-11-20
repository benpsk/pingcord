// Package telegram adapts pingcord notifications to the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benpsk/pingcord/internal/discord"
)

// Client delivers private GitHub notifications and silent admin audit messages.
type Client struct {
	baseURL, token string
	admins         []string
	http           *http.Client
	mu             sync.Mutex
}

// NewClient returns a Telegram sender; a nil client uses a ten-second timeout.
func NewClient(baseURL, token string, adminIDs []string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, admins: append([]string(nil), adminIDs...), http: hc}
}

var mentionRE = regexp.MustCompile(`<@([0-9]+)>`)

func limitedText(s string, limit int) string {
	return html.EscapeString(truncateUnits(strings.ReplaceAll(s, "@", "@\u200b"), limit))
}

// Shared envelopes use Discord mention syntax. In private messages render IDs
// as ordinary text, never as mention links.
func renderContent(content string) string {
	return limitedText(mentionRE.ReplaceAllString(content, "User $1"), 2400)
}

func textUnits(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

// Render converts the shared envelope into Telegram HTML. GitHub-controlled
// text is escaped and IDs are rendered without mention links.
func Render(msg discord.Message) string {
	var b strings.Builder
	b.WriteString(renderContent(msg.Content))
	for _, e := range msg.Embeds[:min(len(msg.Embeds), 1)] {
		b.WriteString("\n\n")
		if e.Author != nil {
			b.WriteString(limitedText(e.Author.Name, 100) + "\n")
		}
		b.WriteString("<b>" + limitedText(e.Title, 512) + "</b>\n")
		b.WriteString(limitedText(e.Description, 600))
		if u, err := url.Parse(e.URL); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
			b.WriteString("\n" + `<a href="` + html.EscapeString(e.URL) + `">Open on GitHub</a>`)
		}
		if e.Footer != nil {
			b.WriteString("\n" + limitedText(e.Footer.Text, 200))
		}
	}
	return strings.TrimSpace(b.String())
}

// Send sends silent audit messages privately to configured admins. GitHub
// notifications must use SendDirect so there is no shared destination fallback.
func (c *Client) Send(ctx context.Context, msg discord.Message) error {
	if len(msg.Embeds) != 0 {
		return errors.New("telegram: notifications require an explicit recipient")
	}
	var errs []error
	seen := map[string]bool{}
	for _, id := range c.admins {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := c.send(ctx, id, Render(msg), true, true); err != nil {
			errs = append(errs, fmt.Errorf("audit to %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// SendDirect sends one recipient their own notification and reasons.
func (c *Client) SendDirect(ctx context.Context, msg discord.Message, userID string) error {
	msg.Content = strings.TrimPrefix(msg.Content, "<@"+userID+"> ")
	msg.Content = "You: " + msg.Content
	return c.send(ctx, userID, Render(msg), true, false)
}

// Reply sends a private command response without interpreting user text as HTML.
func (c *Client) Reply(ctx context.Context, chatID, text string) error {
	text = mentionRE.ReplaceAllString(text, "User $1")
	text = strings.NewReplacer("**", "", "`", "").Replace(text)
	text = truncateUnits(text, 4096)
	return c.send(ctx, chatID, text, false, false)
}

func (c *Client) send(ctx context.Context, chatID, text string, formatted, silent bool) error {
	if id, err := strconv.ParseInt(chatID, 10, 64); err != nil || id <= 0 || strconv.FormatInt(id, 10) != chatID {
		return errors.New("telegram: a positive private user ID is required")
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "disable_notification": silent, "link_preview_options": map[string]bool{"is_disabled": true}}
	if formatted {
		payload["parse_mode"] = "HTML"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("telegram: encode message")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/sendMessage", bytes.NewReader(body))
		if err != nil {
			return errors.New("telegram: invalid API URL")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			// Transport errors can contain the URL, which embeds the bot token.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("telegram: API request failed")
		}
		var result struct {
			OK         bool `json:"ok"`
			Parameters struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result)
		_ = resp.Body.Close()
		if decodeErr == nil && result.OK && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		wait := result.Parameters.RetryAfter
		if resp.StatusCode == 429 && attempt == 0 && wait > 0 && wait <= 5 {
			t := time.NewTimer(time.Duration(wait) * time.Second)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			continue
		}
		return fmt.Errorf("telegram: sendMessage failed (HTTP %d)", resp.StatusCode)
	}
	return errors.New("telegram: sendMessage failed")
}

// truncateUnits measures UTF-16 code units. Truncate before rendering so HTML
// is never cut in half. Reserve one unit for the truncation marker.
func truncateUnits(s string, limit int) string {
	if textUnits(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > limit-1 {
			return s[:i] + "…"
		}
		units += size
	}
	return s
}

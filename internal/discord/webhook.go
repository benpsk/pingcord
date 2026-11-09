// Package discord sends messages to a Discord channel webhook.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultTimeout   = 10 * time.Second
	maxRateLimitWait = 5 * time.Second
	maxErrorBody     = 4 << 10
)

// Message is a Discord webhook execute payload.
type Message struct {
	Content         string          `json:"content,omitempty"`
	Embeds          []Embed         `json:"embeds,omitempty"`
	AllowedMentions AllowedMentions `json:"allowed_mentions"`
}

// AllowedMentions restricts who a message may ping. Parse must be an empty
// list (not null) so that @everyone, roles and arbitrary users are never
// pinged; only IDs in Users are.
type AllowedMentions struct {
	Parse []string `json:"parse"`
	Users []string `json:"users,omitempty"`
}

// Embed is a Discord rich embed.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	URL         string       `json:"url,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
}

// EmbedAuthor is shown at the top of an embed.
type EmbedAuthor struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedFooter is shown at the bottom of an embed.
type EmbedFooter struct {
	Text string `json:"text"`
}

// WebhookClient posts messages to one channel webhook.
type WebhookClient struct {
	url   string
	http  *http.Client
	sleep func(context.Context, time.Duration) error
}

// NewWebhookClient returns a client for the webhook URL. A nil httpClient
// gets a client with a sensible timeout.
func NewWebhookClient(webhookURL string, httpClient *http.Client) *WebhookClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &WebhookClient{url: webhookURL, http: httpClient, sleep: sleepCtx}
}

// Send posts msg. On a rate limit it waits (up to maxRateLimitWait) and
// retries once. Errors never include the webhook URL, which embeds a token.
func (c *WebhookClient) Send(ctx context.Context, msg Message) error {
	if msg.AllowedMentions.Parse == nil {
		msg.AllowedMentions.Parse = []string{}
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("discord: encode message: %w", err)
	}

	wait, err := c.post(ctx, body)
	if wait <= 0 || wait > maxRateLimitWait {
		return err
	}
	if err := c.sleep(ctx, wait); err != nil {
		return fmt.Errorf("discord: waiting for rate limit: %w", err)
	}
	_, err = c.post(ctx, body)
	return err
}

// post sends one request. When rate limited it returns the suggested wait
// alongside the error.
func (c *WebhookClient) post(ctx context.Context, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("discord: invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pingcord (https://github.com/benpsk/pingcord)")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("discord: send webhook: %w", stripURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := retryAfter(resp.Header, respBody)
		return wait, fmt.Errorf("discord: rate limited (429), retry after %s", wait)
	default:
		return 0, fmt.Errorf("discord: webhook returned %d: %s", resp.StatusCode, apiMessage(respBody))
	}
}

// stripURL removes the request URL (and its token) from transport errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func retryAfter(h http.Header, body []byte) time.Duration {
	var rl struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &rl) == nil && rl.RetryAfter > 0 {
		return time.Duration(rl.RetryAfter * float64(time.Second))
	}
	if secs, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func apiMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	return string(bytes.TrimSpace(body))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

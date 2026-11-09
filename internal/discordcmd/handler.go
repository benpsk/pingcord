// Package discordcmd implements the /admin slash commands over Discord's HTTP
// interactions endpoint: request signature verification, admin checks, link
// management and autocomplete.
package discordcmd

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/store"
)

// Discord interaction and response types.
const (
	typePing               = 1
	typeApplicationCommand = 2
	typeAutocomplete       = 4

	responsePong           = 1
	responseChannelMessage = 4
	responseAutocomplete   = 8

	flagEphemeral = 1 << 6
)

const (
	maxBody         = 1 << 20
	maxClockSkew    = 5 * time.Minute
	commandTimeout  = 2500 * time.Millisecond // Discord requires a reply within 3s
	announceTimeout = 10 * time.Second
	adminCommand    = "admin"
	msgNotAdmin     = "Only pingcord admins can use this command."
	msgInternalErr  = "Something went wrong; check the pingcord logs."
	msgUnknown      = "Unknown command."
)

// Store is the persistence the admin commands need.
type Store interface {
	UpsertLink(ctx context.Context, l store.Link) ([]store.Link, error)
	DeleteLinkByDiscordID(ctx context.Context, discordID string) (store.Link, error)
	ListLinks(ctx context.Context) ([]store.Link, error)
	Ignore(ctx context.Context, githubID int64, login, createdBy string) error
	Unignore(ctx context.Context, login string) (store.Ignored, error)
	ListIgnored(ctx context.Context) ([]store.Ignored, error)
}

// Directory resolves GitHub users.
type Directory interface {
	LookupUser(ctx context.Context, login string) (githubapi.User, error)
	OrgMembers(ctx context.Context) ([]githubapi.User, error)
}

// Announcer posts audit messages to the channel.
type Announcer interface {
	Send(ctx context.Context, msg discord.Message) error
}

// Config holds the handler's settings.
type Config struct {
	PublicKey ed25519.PublicKey
	GuildID   string
	Org       string
	IsAdmin   func(discordID string) bool
}

// Handler serves POST /discord/interactions.
type Handler struct {
	cfg   Config
	store Store
	dir   Directory
	ann   Announcer
	log   *slog.Logger
	now   func() time.Time

	background sync.WaitGroup // in-flight audit announcements
}

// Wait blocks until background audit announcements have finished.
func (h *Handler) Wait() { h.background.Wait() }

// Option configures a Handler.
type Option func(*Handler)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(h *Handler) { h.log = l } }

// WithClock overrides the time source (for tests).
func WithClock(now func() time.Time) Option { return func(h *Handler) { h.now = now } }

// NewHandler returns an interactions handler.
func NewHandler(cfg Config, st Store, dir Directory, ann Announcer, opts ...Option) *Handler {
	h := &Handler{cfg: cfg, store: st, dir: dir, ann: ann, log: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

type option struct {
	Name    string          `json:"name"`
	Type    int             `json:"type"`
	Value   json.RawMessage `json:"value"`
	Focused bool            `json:"focused"`
	Options []option        `json:"options"`
}

func (o option) stringValue() string {
	var s string
	if json.Unmarshal(o.Value, &s) == nil {
		return s
	}
	return ""
}

type interaction struct {
	Type    int    `json:"type"`
	GuildID string `json:"guild_id"`
	Member  *struct {
		User discordUser `json:"user"`
	} `json:"member"`
	Data struct {
		Name     string   `json:"name"`
		Options  []option `json:"options"`
		Resolved struct {
			Users map[string]discordUser `json:"users"`
		} `json:"resolved"`
	} `json:"data"`
}

// subcommand returns the /admin subcommand and its options.
func (in *interaction) subcommand() (string, []option) {
	if in.Data.Name != adminCommand || len(in.Data.Options) == 0 {
		return "", nil
	}
	sub := in.Data.Options[0]
	return sub.Name, sub.Options
}

// callerID returns the invoking user in a guild, or "" outside one.
func (in *interaction) callerID() string {
	if in.Member == nil {
		return ""
	}
	return in.Member.User.ID
}

func (h *Handler) isAuthorized(in *interaction) bool {
	caller := in.callerID()
	return caller != "" && in.GuildID == h.cfg.GuildID && h.cfg.IsAdmin(caller)
}

// ServeHTTP verifies and dispatches an interaction.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	if !h.verify(r.Header.Get("X-Signature-Ed25519"), r.Header.Get("X-Signature-Timestamp"), body) {
		http.Error(w, "invalid request signature", http.StatusUnauthorized)
		return
	}

	var in interaction
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "malformed interaction", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	defer cancel()

	switch in.Type {
	case typePing:
		writeJSON(w, map[string]int{"type": responsePong})
	case typeApplicationCommand:
		writeJSON(w, reply(h.command(ctx, &in)))
	case typeAutocomplete:
		writeJSON(w, autocompleteResponse(h.autocomplete(ctx, &in)))
	default:
		http.Error(w, "unsupported interaction type", http.StatusBadRequest)
	}
}

// verify checks Discord's Ed25519 signature over timestamp+body and rejects
// timestamps outside maxClockSkew to limit replay.
func (h *Handler) verify(sigHex, timestamp string, body []byte) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize || len(h.cfg.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if skew := h.now().Sub(time.Unix(secs, 0)); skew > maxClockSkew || skew < -maxClockSkew {
		return false
	}
	msg := make([]byte, 0, len(timestamp)+len(body))
	msg = append(append(msg, timestamp...), body...)
	return ed25519.Verify(h.cfg.PublicKey, msg, sig)
}

type messageData struct {
	Content         string                  `json:"content"`
	Flags           int                     `json:"flags"`
	AllowedMentions discord.AllowedMentions `json:"allowed_mentions"`
}

// reply is an ephemeral message that can never ping anyone.
func reply(content string) any {
	return struct {
		Type int         `json:"type"`
		Data messageData `json:"data"`
	}{
		Type: responseChannelMessage,
		Data: messageData{Content: content, Flags: flagEphemeral, AllowedMentions: discord.AllowedMentions{Parse: []string{}}},
	}
}

type choice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func autocompleteResponse(choices []choice) any {
	if choices == nil {
		choices = []choice{}
	}
	return struct {
		Type int `json:"type"`
		Data struct {
			Choices []choice `json:"choices"`
		} `json:"data"`
	}{Type: responseAutocomplete, Data: struct {
		Choices []choice `json:"choices"`
	}{Choices: choices}}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

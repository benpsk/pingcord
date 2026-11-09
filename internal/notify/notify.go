// Package notify decides which linked Discord users a GitHub event concerns
// and sends them a single message that pings only them.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/githubhook"
	"github.com/benpsk/pingcord/internal/store"
)

// Store is the persistence the notifier needs.
type Store interface {
	RefreshLogins(ctx context.Context, logins map[int64]string) error
	LinksByGitHubIDs(ctx context.Context, ids []int64) (map[int64]store.Link, error)
	IgnoredIDs(ctx context.Context, ids []int64) (map[int64]bool, error)
}

// Directory resolves GitHub identities.
type Directory interface {
	ResolveLogins(ctx context.Context, logins []string) (map[string]int64, error)
	TeamMembers(ctx context.Context, team string) ([]githubapi.User, error)
}

// Sender delivers a Discord message.
type Sender interface {
	Send(ctx context.Context, msg discord.Message) error
}

// Result describes what Handle did, for logging.
type Result struct {
	Pinged   []string // Discord IDs pinged
	Unlinked []string // GitHub logins that would have been pinged but have no link
}

// Notifier turns events into Discord pings.
type Notifier struct {
	store      Store
	dir        Directory
	sender     Sender
	ignoreBots bool
	log        *slog.Logger
	now        func() time.Time
}

// Option configures a Notifier.
type Option func(*Notifier)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(n *Notifier) { n.log = l } }

// WithClock overrides the time source (for tests).
func WithClock(now func() time.Time) Option { return func(n *Notifier) { n.now = now } }

// WithIgnoreBots controls whether bot actors and bot recipients are skipped
// (default true).
func WithIgnoreBots(v bool) Option { return func(n *Notifier) { n.ignoreBots = v } }

// New returns a Notifier.
func New(st Store, dir Directory, sender Sender, opts ...Option) *Notifier {
	n := &Notifier{store: st, dir: dir, sender: sender, ignoreBots: true, log: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// candidate is a GitHub user who may be pinged, with one reason.
type candidate struct {
	id     int64
	login  string
	isBot  bool
	reason string
}

// recipient is a Discord user to ping with all of their reasons.
type recipient struct {
	discordID string
	reasons   []string
}

// Handle notifies everyone ev concerns. Lookup problems for individual users
// are logged and skipped; store and Discord failures are returned so the
// webhook delivery fails and can be redelivered from GitHub.
func (n *Notifier) Handle(ctx context.Context, ev githubhook.Event) (Result, error) {
	if len(ev.Users) > 0 {
		if err := n.store.RefreshLogins(ctx, ev.Users); err != nil {
			n.log.WarnContext(ctx, "refresh logins failed", "error", err)
		}
	}
	if n.ignoreBots && ev.Actor.IsBot {
		return Result{}, nil
	}

	cands, err := n.filter(ctx, ev.Actor, n.candidates(ctx, ev))
	if err != nil {
		return Result{}, err
	}
	recipients, unlinked, err := n.link(ctx, cands)
	if err != nil {
		return Result{}, err
	}
	res := Result{Unlinked: unlinked}
	if len(recipients) == 0 {
		return res, nil
	}

	if err := n.sender.Send(ctx, buildMessage(ev, recipients, n.now())); err != nil {
		return res, fmt.Errorf("send discord message: %w", err)
	}
	for _, r := range recipients {
		res.Pinged = append(res.Pinged, r.discordID)
	}
	return res, nil
}

// candidates lists everyone the event concerns, in display order: role
// targets, then @user mentions, then @team mentions.
func (n *Notifier) candidates(ctx context.Context, ev githubhook.Event) []candidate {
	var out []candidate
	for _, t := range ev.Targets {
		if t.Team != "" {
			out = append(out, n.teamCandidates(ctx, t.Team, string(t.Reason)+" via @"+t.Team)...)
			continue
		}
		out = append(out, candidate{id: t.User.ID, login: t.User.Login, isBot: t.User.IsBot, reason: string(t.Reason)})
	}

	if len(ev.Mentions.Users) > 0 {
		ids, err := n.dir.ResolveLogins(ctx, ev.Mentions.Users)
		if err != nil {
			n.log.WarnContext(ctx, "some mentions could not be resolved", "error", err)
		}
		for _, login := range ev.Mentions.Users {
			if id, ok := ids[login]; ok {
				out = append(out, candidate{id: id, login: login, reason: string(githubhook.ReasonMentioned)})
			}
		}
	}

	for _, team := range ev.Mentions.Teams {
		out = append(out, n.teamCandidates(ctx, team, string(githubhook.ReasonMentioned)+" via @"+team)...)
	}
	return out
}

func (n *Notifier) teamCandidates(ctx context.Context, team, reason string) []candidate {
	members, err := n.dir.TeamMembers(ctx, team)
	if err != nil {
		n.log.WarnContext(ctx, "team members unavailable", "team", team, "error", err)
		return nil
	}
	out := make([]candidate, 0, len(members))
	for _, m := range members {
		out = append(out, candidate{id: m.ID, login: m.Login, reason: reason})
	}
	return out
}

// filter drops the actor, bots (when configured) and ignored users.
func (n *Notifier) filter(ctx context.Context, actor githubhook.User, cands []candidate) ([]candidate, error) {
	ids := make([]int64, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.id)
	}
	ignored, err := n.store.IgnoredIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load ignored users: %w", err)
	}

	out := make([]candidate, 0, len(cands))
	for _, c := range cands {
		isBot := c.isBot || strings.HasSuffix(c.login, "[bot]")
		if c.id <= 0 || c.id == actor.ID || ignored[c.id] || (n.ignoreBots && isBot) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// link maps candidates to Discord users, grouping reasons per person in
// first-seen order, and reports candidates that have no link.
func (n *Notifier) link(ctx context.Context, cands []candidate) ([]recipient, []string, error) {
	ids := make([]int64, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.id)
	}
	links, err := n.store.LinksByGitHubIDs(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("load links: %w", err)
	}

	var (
		recipients []recipient
		unlinked   []string
		index      = map[string]int{}
		seenLogin  = map[string]bool{}
	)
	for _, c := range cands {
		l, ok := links[c.id]
		if !ok {
			if !seenLogin[c.login] {
				seenLogin[c.login] = true
				unlinked = append(unlinked, c.login)
			}
			continue
		}
		i, ok := index[l.DiscordID]
		if !ok {
			i = len(recipients)
			index[l.DiscordID] = i
			recipients = append(recipients, recipient{discordID: l.DiscordID})
		}
		if !slices.Contains(recipients[i].reasons, c.reason) {
			recipients[i].reasons = append(recipients[i].reasons, c.reason)
		}
	}
	return recipients, unlinked, nil
}

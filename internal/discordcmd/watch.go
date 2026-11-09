package discordcmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/benpsk/pingcord/internal/githubhook"
	"github.com/benpsk/pingcord/internal/store"
)

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// levelDescriptions are shown in replies and as the level option's choices.
var levelDescriptions = map[string]string{
	store.LevelNewPRs: "new PRs",
	store.LevelPRs:    "PRs (new, merged/closed, reviews)",
	store.LevelAll:    "all activity",
}

func (h *Handler) watch(ctx context.Context, in *interaction, admin string, opts []option) string {
	discordID, levelName := optionValue(opts, "user"), optionValue(opts, "level")
	if discordID == "" || levelName == "" {
		return "Missing `user` or `level` option."
	}
	if u, ok := in.Data.Resolved.Users[discordID]; ok && u.Bot {
		return "That Discord account is a bot; watchers must be people."
	}
	if _, ok := githubhook.ParseWatchLevel(levelName); !ok {
		return fmt.Sprintf("Unknown level `%s`; pick new-prs, prs or all.", sanitize(levelName))
	}
	repo, msg := h.repoOption(opts)
	if msg != "" {
		return msg
	}
	if linked, msg := h.isLinked(ctx, discordID); !linked {
		return msg
	}

	if err := h.store.SetWatch(ctx, store.Watch{DiscordID: discordID, Repo: repo, Level: levelName, CreatedBy: admin}); err != nil {
		return h.internal(ctx, "watch", err)
	}
	what, where := levelDescriptions[levelName], scope(repo)
	h.announce(fmt.Sprintf("👀 <@%s> set <@%s> to watch %s in %s", admin, discordID, what, where))
	return fmt.Sprintf("<@%s> now watches %s in %s.", discordID, what, where)
}

func (h *Handler) unwatch(ctx context.Context, admin string, opts []option) string {
	discordID := optionValue(opts, "user")
	if discordID == "" {
		return "Missing `user` option."
	}
	repo, msg := h.repoOption(opts)
	if msg != "" {
		return msg
	}

	removed, err := h.store.RemoveWatch(ctx, discordID, repo)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("<@%s> isn't watching %s.", discordID, scope(repo))
	}
	if err != nil {
		return h.internal(ctx, "unwatch", err)
	}
	where := scope(removed.Repo)
	h.announce(fmt.Sprintf("🙈 <@%s> stopped <@%s> watching %s", admin, discordID, where))
	return fmt.Sprintf("<@%s> no longer watches %s.", discordID, where)
}

// repoOption reads the optional repo ("api" or "org/api"; empty = whole
// org). On failure it returns the reply to show instead.
func (h *Handler) repoOption(opts []option) (string, string) {
	repo := optionValue(opts, "repo")
	if org, name, ok := strings.Cut(repo, "/"); ok {
		if !strings.EqualFold(org, h.cfg.Org) {
			return "", fmt.Sprintf("Only repositories in %s can be watched.", h.cfg.Org)
		}
		repo = name
	}
	if repo != "" && !repoNameRe.MatchString(repo) {
		return "", fmt.Sprintf("`%s` isn't a valid repository name.", sanitize(repo))
	}
	return repo, ""
}

func (h *Handler) isLinked(ctx context.Context, discordID string) (bool, string) {
	links, err := h.store.ListLinks(ctx)
	if err != nil {
		return false, h.internal(ctx, "list links", err)
	}
	for _, l := range links {
		if l.DiscordID == discordID {
			return true, ""
		}
	}
	return false, fmt.Sprintf("<@%s> isn't linked yet. Link them first with `/admin link`.", discordID)
}

func scope(repo string) string {
	if repo == "" {
		return "the whole org"
	}
	return "`" + repo + "`"
}

// watcherLines renders "• <@id> new-prs (org) · all (api)", one per user.
func watcherLines(watches []store.Watch) []string {
	var (
		order []string
		parts = map[string][]string{}
	)
	for _, w := range watches {
		if _, ok := parts[w.DiscordID]; !ok {
			order = append(order, w.DiscordID)
		}
		where := "org"
		if w.Repo != "" {
			where = w.Repo
		}
		parts[w.DiscordID] = append(parts[w.DiscordID], fmt.Sprintf("%s (%s)", w.Level, where))
	}
	lines := make([]string, len(order))
	for i, id := range order {
		lines[i] = fmt.Sprintf("• <@%s> %s", id, strings.Join(parts[id], " · "))
	}
	return lines
}

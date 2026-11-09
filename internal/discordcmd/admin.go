package discordcmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/store"
)

const maxReplyRunes = 2000

var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// command runs an /admin subcommand and returns the ephemeral reply text.
func (h *Handler) command(ctx context.Context, in *interaction) string {
	if !h.isAuthorized(in) {
		h.log.WarnContext(ctx, "unauthorized admin command", "caller", in.callerID(), "guild", in.GuildID)
		return msgNotAdmin
	}
	sub, opts := in.subcommand()
	admin := in.callerID()
	h.log.InfoContext(ctx, "admin command", "caller", admin, "subcommand", sub)

	switch sub {
	case "link":
		return h.link(ctx, in, admin, opts)
	case "unlink":
		return h.unlink(ctx, admin, optionValue(opts, "user"))
	case "list":
		return h.list(ctx)
	case "unlinked":
		return h.unlinked(ctx)
	case "ignore":
		return h.ignore(ctx, admin, optionValue(opts, "github"))
	case "unignore":
		return h.unignore(ctx, admin, optionValue(opts, "github"))
	case "watch":
		return h.watch(ctx, in, admin, opts)
	case "unwatch":
		return h.unwatch(ctx, admin, opts)
	}
	return msgUnknown
}

func (h *Handler) link(ctx context.Context, in *interaction, admin string, opts []option) string {
	discordID, login := optionValue(opts, "user"), optionValue(opts, "github")
	if discordID == "" || login == "" {
		return "Missing `user` or `github` option."
	}
	if u, ok := in.Data.Resolved.Users[discordID]; ok && u.Bot {
		return "That Discord account is a bot; link a person instead."
	}
	gh, msg := h.lookup(ctx, login)
	if msg != "" {
		return msg
	}

	replaced, err := h.store.UpsertLink(ctx, store.Link{GitHubID: gh.ID, GitHubLogin: gh.Login, DiscordID: discordID, CreatedBy: admin})
	if err != nil {
		return h.internal(ctx, "link", err)
	}
	note := replacedNote(replaced)
	h.announce(fmt.Sprintf("🔗 <@%s> linked GitHub `%s` → <@%s>%s", admin, gh.Login, discordID, note))
	return fmt.Sprintf("Linked `%s` → <@%s>%s. They'll be pinged when GitHub needs them.", gh.Login, discordID, note)
}

// replacedNote describes links removed by a relink, e.g. " (replaced `bob` → <@1>)".
func replacedNote(replaced []store.Link) string {
	if len(replaced) == 0 {
		return ""
	}
	parts := make([]string, len(replaced))
	for i, l := range replaced {
		parts[i] = fmt.Sprintf("`%s` → <@%s>", l.GitHubLogin, l.DiscordID)
	}
	return " (replaced " + strings.Join(parts, ", ") + ")"
}

func (h *Handler) unlink(ctx context.Context, admin, discordID string) string {
	if discordID == "" {
		return "Missing `user` option."
	}
	l, err := h.store.DeleteLinkByDiscordID(ctx, discordID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("<@%s> isn't linked.", discordID)
	}
	if err != nil {
		return h.internal(ctx, "unlink", err)
	}
	h.announce(fmt.Sprintf("✂️ <@%s> unlinked GitHub `%s` from <@%s>", admin, l.GitHubLogin, discordID))
	return fmt.Sprintf("Unlinked `%s` from <@%s>.", l.GitHubLogin, discordID)
}

func (h *Handler) list(ctx context.Context) string {
	links, err := h.store.ListLinks(ctx)
	if err != nil {
		return h.internal(ctx, "list", err)
	}
	if len(links) == 0 {
		return "No links yet. Use `/admin link` to add one, or `/admin unlinked` to see who's missing."
	}
	lines := make([]string, len(links))
	for i, l := range links {
		lines[i] = fmt.Sprintf("• `%s` → <@%s>", l.GitHubLogin, l.DiscordID)
	}
	watches, err := h.store.ListWatches(ctx)
	if err != nil {
		return h.internal(ctx, "list watches", err)
	}
	if len(watches) > 0 {
		lines = append(append(lines, "", "**Watchers**"), watcherLines(watches)...)
	}
	return bulleted(fmt.Sprintf("**Links (%d)**", len(links)), lines)
}

func (h *Handler) unlinked(ctx context.Context) string {
	members, err := h.dir.OrgMembers(ctx)
	if err != nil {
		return h.internal(ctx, "list org members", err)
	}
	links, err := h.store.ListLinks(ctx)
	if err != nil {
		return h.internal(ctx, "list links", err)
	}
	ignored, err := h.store.ListIgnored(ctx)
	if err != nil {
		return h.internal(ctx, "list ignored", err)
	}

	skip := map[int64]bool{}
	for _, l := range links {
		skip[l.GitHubID] = true
	}
	for _, ig := range ignored {
		skip[ig.GitHubID] = true
	}
	var lines []string
	for _, m := range members {
		if !skip[m.ID] {
			lines = append(lines, fmt.Sprintf("• `%s`", m.Login))
		}
	}
	if len(lines) == 0 {
		return fmt.Sprintf("Everyone in %s is linked (or ignored). 🎉", h.cfg.Org)
	}
	return bulleted(fmt.Sprintf("**%s members without a link (%d)**", h.cfg.Org, len(lines)), lines)
}

func (h *Handler) ignore(ctx context.Context, admin, login string) string {
	gh, msg := h.lookup(ctx, login)
	if msg != "" {
		return msg
	}
	if err := h.store.Ignore(ctx, gh.ID, gh.Login, admin); err != nil {
		return h.internal(ctx, "ignore", err)
	}
	h.announce(fmt.Sprintf("🔕 <@%s> set pingcord to ignore GitHub `%s`", admin, gh.Login))
	return fmt.Sprintf("Ignoring `%s`: pingcord will never ping them.", gh.Login)
}

func (h *Handler) unignore(ctx context.Context, admin, login string) string {
	if !loginRe.MatchString(login) {
		return fmt.Sprintf("`%s` isn't a valid GitHub username.", sanitize(login))
	}
	ig, err := h.store.Unignore(ctx, login)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("`%s` isn't ignored.", login)
	}
	if err != nil {
		return h.internal(ctx, "unignore", err)
	}
	h.announce(fmt.Sprintf("🔔 <@%s> stopped ignoring GitHub `%s`", admin, ig.GitHubLogin))
	return fmt.Sprintf("No longer ignoring `%s`.", ig.GitHubLogin)
}

// lookup validates and resolves a GitHub login. On failure it returns the
// reply to show instead.
func (h *Handler) lookup(ctx context.Context, login string) (githubapi.User, string) {
	if login == "" {
		return githubapi.User{}, "Missing `github` option."
	}
	if !loginRe.MatchString(login) {
		return githubapi.User{}, fmt.Sprintf("`%s` isn't a valid GitHub username.", sanitize(login))
	}
	u, err := h.dir.LookupUser(ctx, login)
	if errors.Is(err, githubapi.ErrNotFound) {
		return githubapi.User{}, fmt.Sprintf("GitHub user `%s` not found.", login)
	}
	if err != nil {
		return githubapi.User{}, h.internal(ctx, "github lookup", err)
	}
	return u, ""
}

func (h *Handler) internal(ctx context.Context, op string, err error) string {
	h.log.ErrorContext(ctx, "admin command failed", "op", op, "error", err)
	return msgInternalErr
}

// announce posts an audit line to the channel in the background, so a slow
// Discord webhook can't push the reply past Discord's 3s interaction deadline.
// Mentions in it render but never ping.
func (h *Handler) announce(content string) {
	msg := discord.Message{Content: content, AllowedMentions: discord.AllowedMentions{Parse: []string{}}}
	h.background.Add(1)
	go func() {
		defer h.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
		defer cancel()
		if err := h.ann.Send(ctx, msg); err != nil {
			h.log.Error("audit announcement failed; the change was applied", "content", content, "error", err)
		}
	}()
}

func optionValue(opts []option, name string) string {
	i := slices.IndexFunc(opts, func(o option) bool { return o.Name == name })
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(opts[i].stringValue())
}

// bulleted joins lines under a header, truncating to Discord's limit.
func bulleted(header string, lines []string) string {
	var b strings.Builder
	b.WriteString(header)
	runes := utf8.RuneCountInString(header)
	for i, line := range lines {
		n := utf8.RuneCountInString(line) + 1
		if runes+n > maxReplyRunes-30 {
			fmt.Fprintf(&b, "\n…and %d more", len(lines)-i)
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
		runes += n
	}
	return b.String()
}

// sanitize makes arbitrary input safe to echo inside inline code.
func sanitize(s string) string {
	s = strings.NewReplacer("`", "'", "\n", " ", "@", "@\u200b").Replace(s)
	if utf8.RuneCountInString(s) > 50 {
		s = string([]rune(s)[:50]) + "…"
	}
	return s
}

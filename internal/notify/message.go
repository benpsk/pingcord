package notify

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubhook"
)

// Discord limits (with headroom where useful).
const (
	maxContentRunes = 2000
	maxAllowedUsers = 100
	maxTitleRunes   = 256
	maxExcerptRunes = 300
	moreLineReserve = 40
	embedColor      = 0x2F81F7
)

var (
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)
	blankLinesRe  = regexp.MustCompile(`\n{3,}`)
)

var kindLabels = map[githubhook.Kind]string{
	githubhook.KindPR:         "PR",
	githubhook.KindIssue:      "Issue",
	githubhook.KindDiscussion: "Discussion",
	githubhook.KindCommit:     "Commit",
	githubhook.KindRelease:    "Release",
}

// buildMessage renders one Discord message: content holds one line per
// recipient (the only pings), the embed holds the context.
func buildMessage(ev githubhook.Event, recipients []recipient, now time.Time) discord.Message {
	content, allowed := renderContent(recipients)

	embed := discord.Embed{
		Title:       embedTitle(ev),
		URL:         ev.URL,
		Description: cleanExcerpt(ev.Excerpt),
		Color:       embedColor,
		Footer:      &discord.EmbedFooter{Text: footerText(ev)},
		Timestamp:   now.UTC().Format(time.RFC3339),
	}
	if ev.Actor.Login != "" {
		embed.Author = &discord.EmbedAuthor{Name: ev.Actor.Login, URL: "https://github.com/" + ev.Actor.Login}
		if ev.Actor.ID > 0 {
			embed.Author.IconURL = fmt.Sprintf("https://avatars.githubusercontent.com/u/%d", ev.Actor.ID)
		}
	}

	return discord.Message{
		Content:         content,
		Embeds:          []discord.Embed{embed},
		AllowedMentions: discord.AllowedMentions{Parse: []string{}, Users: allowed},
	}
}

// renderContent writes "<@id> reason, reason" lines within Discord's content
// and allowed-mentions limits. Only users whose line fits are allowed to be
// pinged; the rest are summarized.
func renderContent(recipients []recipient) (string, []string) {
	var (
		b       strings.Builder
		allowed []string
		runes   int
	)
	for i, r := range recipients {
		line := fmt.Sprintf("<@%s> %s", r.discordID, strings.Join(r.reasons, ", "))
		lineRunes := utf8.RuneCountInString(line) + 1
		if len(allowed) == maxAllowedUsers || runes+lineRunes > maxContentRunes-moreLineReserve {
			fmt.Fprintf(&b, "\n…and %d more", len(recipients)-i)
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		runes += lineRunes
		allowed = append(allowed, r.discordID)
	}
	return b.String(), allowed
}

func embedTitle(ev githubhook.Event) string {
	s := ev.Subject
	var what string
	switch {
	case s.Kind == "":
		what = ev.Name
	case s.Number > 0:
		what = fmt.Sprintf("%s #%d: %s", kindLabel(s.Kind), s.Number, s.Title)
	default:
		what = strings.TrimSpace(kindLabel(s.Kind) + " " + s.Title)
	}
	return truncateRunes(fmt.Sprintf("[%s] %s", ev.Repo, what), maxTitleRunes)
}

func kindLabel(k githubhook.Kind) string {
	if l, ok := kindLabels[k]; ok {
		return l
	}
	r, size := utf8.DecodeRuneInString(string(k))
	return string(unicode.ToUpper(r)) + string(k)[size:]
}

func footerText(ev githubhook.Event) string {
	if ev.Action == "" {
		return ev.Name
	}
	return ev.Name + " · " + ev.Action
}

// cleanExcerpt prepares user-written GitHub text for an embed: drops HTML
// comments (PR templates), collapses blank lines, truncates, and inserts a
// zero-width space after every "@" so nothing in it can render as a ping.
func cleanExcerpt(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = blankLinesRe.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	s = truncateRunes(s, maxExcerptRunes)
	return strings.ReplaceAll(s, "@", "@\u200b")
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return strings.TrimRightFunc(string(runes[:limit-1]), unicode.IsSpace) + "…"
}

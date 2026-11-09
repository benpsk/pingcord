package notify

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/benpsk/pingcord/internal/githubhook"
)

var fixedNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestBuildMessageEmbed(t *testing.T) {
	ev := prComment()
	recipients := []recipient{{discordID: "100", reasons: []string{"commented on your PR"}}}

	msg := buildMessage(ev, recipients, fixedNow)

	if len(msg.Embeds) != 1 {
		t.Fatalf("embeds = %d", len(msg.Embeds))
	}
	e := msg.Embeds[0]
	if e.Title != "[my-org/api] PR #12: Add login flow" {
		t.Errorf("Title = %q", e.Title)
	}
	if e.URL != "https://github.com/my-org/api/pull/12#issuecomment-1" {
		t.Errorf("URL = %q", e.URL)
	}
	if e.Author == nil || e.Author.Name != "bob" || e.Author.URL != "https://github.com/bob" {
		t.Errorf("Author = %+v", e.Author)
	}
	if e.Footer == nil || e.Footer.Text != "issue_comment · created" {
		t.Errorf("Footer = %+v", e.Footer)
	}
	if e.Timestamp != "2026-11-09T12:00:00Z" {
		t.Errorf("Timestamp = %q", e.Timestamp)
	}
	if !strings.Contains(e.Description, "LGTM @\u200bcarol") {
		t.Errorf("Description should contain defanged excerpt, got %q", e.Description)
	}
}

func TestEmbedTitleVariants(t *testing.T) {
	tests := []struct {
		subject githubhook.Subject
		want    string
	}{
		{githubhook.Subject{Kind: githubhook.KindIssue, Number: 7, Title: "Crash"}, "[r] Issue #7: Crash"},
		{githubhook.Subject{Kind: githubhook.KindDiscussion, Number: 3, Title: "Roadmap"}, "[r] Discussion #3: Roadmap"},
		{githubhook.Subject{Kind: githubhook.KindCommit, Title: "0123456"}, "[r] Commit 0123456"},
		{githubhook.Subject{Kind: githubhook.KindRelease, Title: "v1.2.0"}, "[r] Release v1.2.0"},
		{githubhook.Subject{Kind: "milestone", Title: "v2"}, "[r] Milestone v2"},
		{githubhook.Subject{}, "[r] push"},
	}
	for _, tt := range tests {
		ev := githubhook.Event{Name: "push", Repo: "r", Subject: tt.subject}
		if got := embedTitle(ev); got != tt.want {
			t.Errorf("embedTitle(%+v) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestEmbedTitleTruncated(t *testing.T) {
	ev := githubhook.Event{Repo: "r", Subject: githubhook.Subject{Kind: githubhook.KindPR, Number: 1, Title: strings.Repeat("é", 400)}}

	if got := embedTitle(ev); utf8.RuneCountInString(got) > maxTitleRunes {
		t.Fatalf("title has %d runes", utf8.RuneCountInString(got))
	}
}

func TestCleanExcerpt(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "   ", ""},
		{"strips html comments", "<!-- template -->\nReal text", "Real text"},
		{"collapses blank lines", "a\n\n\n\n\nb", "a\n\nb"},
		{"defangs at-signs", "@everyone and @alice", "@\u200beveryone and @\u200balice"},
		{"defangs discord mention syntax", "<@123> <@&456>", "<@\u200b123> <@\u200b&456>"},
		{"normalizes CRLF", "a\r\nb", "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanExcerpt(tt.in); got != tt.want {
				t.Errorf("cleanExcerpt(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanExcerptTruncates(t *testing.T) {
	got := cleanExcerpt(strings.Repeat("word ", 200))

	if n := utf8.RuneCountInString(got); n > maxExcerptRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("len=%d suffix=%q", n, got[len(got)-5:])
	}
}

func TestContentRespectsDiscordLimits(t *testing.T) {
	var recipients []recipient
	for i := range 150 {
		recipients = append(recipients, recipient{discordID: strings.Repeat("9", 18) + string(rune('a'+i%26)), reasons: []string{"mentioned via @my-org/some-really-long-team-name"}})
	}

	msg := buildMessage(githubhook.Event{Repo: "r"}, recipients, fixedNow)

	if utf8.RuneCountInString(msg.Content) > maxContentRunes {
		t.Fatalf("content has %d runes", utf8.RuneCountInString(msg.Content))
	}
	if len(msg.AllowedMentions.Users) > maxAllowedUsers {
		t.Fatalf("allowed users = %d", len(msg.AllowedMentions.Users))
	}
	for _, id := range msg.AllowedMentions.Users {
		if !strings.Contains(msg.Content, "<@"+id+">") {
			t.Fatalf("allowed user %s not in content", id)
		}
	}
	if !strings.Contains(msg.Content, "more") {
		t.Fatal("truncated content should say how many more")
	}
}

func TestBuildMessageWithoutActorOrExcerpt(t *testing.T) {
	msg := buildMessage(githubhook.Event{Repo: "r", Name: "x"}, []recipient{{discordID: "1", reasons: []string{"assigned"}}}, fixedNow)

	if msg.Embeds[0].Author != nil || msg.Embeds[0].Description != "" {
		t.Fatalf("embed = %+v", msg.Embeds[0])
	}
	if msg.Embeds[0].Footer.Text != "x" {
		t.Fatalf("footer = %q", msg.Embeds[0].Footer.Text)
	}
}

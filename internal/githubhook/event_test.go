package githubhook

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var (
	alice = User{ID: 1, Login: "alice"}
	bob   = User{ID: 2, Login: "bob"}
	carol = User{ID: 3, Login: "carol"}
	erin  = User{ID: 5, Login: "erin"}
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestParseRecipients(t *testing.T) {
	tests := []struct {
		event, fixture string
		wantTargets    []Target
		wantUsers      []string
		wantTeams      []string
	}{
		{
			event: "issue_comment", fixture: "issue_comment_created_pr",
			wantTargets: []Target{{User: alice, Reason: ReasonCommentOnPR}},
			wantUsers:   []string{"carol"}, wantTeams: []string{"my-org/qa"},
		},
		{
			event: "issue_comment", fixture: "issue_comment_created_issue",
			wantTargets: []Target{{User: alice, Reason: ReasonCommentOnIssue}},
		},
		{
			// Edit adds @erin; @carol was already there and must not ping again.
			event: "issue_comment", fixture: "issue_comment_edited",
			wantUsers: []string{"erin"},
		},
		{event: "issue_comment", fixture: "issue_comment_deleted"},
		{event: "pull_request", fixture: "pull_request_opened", wantUsers: []string{"dave"}},
		{event: "pull_request", fixture: "pull_request_edited_title"},
		{event: "pull_request", fixture: "pull_request_edited_body", wantUsers: []string{"dave"}},
		{
			event: "pull_request", fixture: "pull_request_review_requested_user",
			wantTargets: []Target{{User: carol, Reason: ReasonReviewRequested}},
		},
		{
			event: "pull_request", fixture: "pull_request_review_requested_team",
			wantTargets: []Target{{Team: "my-org/backend", Reason: ReasonReviewRequested}},
		},
		{
			event: "pull_request", fixture: "pull_request_assigned",
			wantTargets: []Target{{User: erin, Reason: ReasonAssigned}},
		},
		{event: "pull_request", fixture: "pull_request_synchronize"},
		{
			event: "pull_request_review", fixture: "pull_request_review_approved",
			wantTargets: []Target{{User: alice, Reason: ReasonApproved}},
			wantUsers:   []string{"erin"},
		},
		{
			event: "pull_request_review", fixture: "pull_request_review_changes",
			wantTargets: []Target{{User: alice, Reason: ReasonChangesRequested}},
		},
		{
			event: "pull_request_review", fixture: "pull_request_review_commented",
			wantTargets: []Target{{User: alice, Reason: ReasonCommentOnPR}},
		},
		{
			// Inline review comments never ping the PR author (the review does).
			event: "pull_request_review_comment", fixture: "pull_request_review_comment_created",
			wantUsers: []string{"carol"},
		},
		{event: "issues", fixture: "issues_opened", wantUsers: []string{"dave"}},
		{
			event: "issues", fixture: "issues_assigned",
			wantTargets: []Target{{User: carol, Reason: ReasonAssigned}},
		},
		{event: "issues", fixture: "issues_labeled"},
		{
			event: "discussion_comment", fixture: "discussion_comment_created",
			wantTargets: []Target{{User: alice, Reason: ReasonCommentOnDiscussion}},
			wantUsers:   []string{"carol"},
		},
		{event: "commit_comment", fixture: "commit_comment_created", wantUsers: []string{"erin"}},
		{event: "release", fixture: "release_published", wantUsers: []string{"carol", "erin"}},
		// A direct publish sends both created and published; drafts send created.
		// Only published pings, so nobody is pinged twice or for a draft.
		{event: "release", fixture: "release_created"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			ev, err := Parse(tt.event, loadFixture(t, tt.fixture))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if !reflect.DeepEqual(ev.Targets, tt.wantTargets) {
				t.Errorf("Targets = %+v, want %+v", ev.Targets, tt.wantTargets)
			}
			if !equalOrEmpty(ev.Mentions.Users, tt.wantUsers) {
				t.Errorf("Mentions.Users = %q, want %q", ev.Mentions.Users, tt.wantUsers)
			}
			if !equalOrEmpty(ev.Mentions.Teams, tt.wantTeams) {
				t.Errorf("Mentions.Teams = %q, want %q", ev.Mentions.Teams, tt.wantTeams)
			}
		})
	}
}

func TestParseContext(t *testing.T) {
	tests := []struct {
		event, fixture string
		wantActor      User
		wantSubject    Subject
		wantURL        string
		wantExcerpt    string
	}{
		{
			event: "issue_comment", fixture: "issue_comment_created_pr", wantActor: bob,
			wantSubject: Subject{Kind: KindPR, Number: 12, Title: "Add login flow", URL: "https://github.com/my-org/api/pull/12", Author: alice},
			wantURL:     "https://github.com/my-org/api/pull/12#issuecomment-501",
			wantExcerpt: "LGTM @carol, cc @my-org/qa",
		},
		{
			event: "issues", fixture: "issues_assigned", wantActor: bob,
			wantSubject: Subject{Kind: KindIssue, Number: 7, Title: "Crash on start", URL: "https://github.com/my-org/api/issues/7", Author: alice},
			wantURL:     "https://github.com/my-org/api/issues/7",
		},
		{
			event: "pull_request", fixture: "pull_request_review_requested_user", wantActor: alice,
			wantSubject: Subject{Kind: KindPR, Number: 12, Title: "Add login flow", URL: "https://github.com/my-org/api/pull/12", Author: alice},
			wantURL:     "https://github.com/my-org/api/pull/12",
		},
		{
			event: "pull_request_review", fixture: "pull_request_review_commented", wantActor: bob,
			wantSubject: Subject{Kind: KindPR, Number: 12, Title: "Add login flow", URL: "https://github.com/my-org/api/pull/12", Author: alice},
			wantURL:     "https://github.com/my-org/api/pull/12#pullrequestreview-803",
		},
		{
			event: "commit_comment", fixture: "commit_comment_created", wantActor: bob,
			wantSubject: Subject{Kind: KindCommit, Title: "0123456", URL: "https://github.com/my-org/api/commit/0123456#commitcomment-701"},
			wantURL:     "https://github.com/my-org/api/commit/0123456#commitcomment-701",
			wantExcerpt: "@erin this broke CI",
		},
		{
			event: "release", fixture: "release_published", wantActor: bob,
			wantSubject: Subject{Kind: KindRelease, Title: "v1.2.0", URL: "https://github.com/my-org/api/releases/tag/v1.2.0", Author: bob},
			wantURL:     "https://github.com/my-org/api/releases/tag/v1.2.0",
			wantExcerpt: "Thanks @carol and @erin!",
		},
		{
			event: "discussion_comment", fixture: "discussion_comment_created", wantActor: bob,
			wantSubject: Subject{Kind: KindDiscussion, Number: 3, Title: "Roadmap", URL: "https://github.com/my-org/api/discussions/3", Author: alice},
			wantURL:     "https://github.com/my-org/api/discussions/3#discussioncomment-601",
			wantExcerpt: "Agree with @carol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			ev, err := Parse(tt.event, loadFixture(t, tt.fixture))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ev.Repo != "my-org/api" {
				t.Errorf("Repo = %q", ev.Repo)
			}
			if ev.Actor != tt.wantActor {
				t.Errorf("Actor = %+v, want %+v", ev.Actor, tt.wantActor)
			}
			if ev.Subject != tt.wantSubject {
				t.Errorf("Subject = %+v, want %+v", ev.Subject, tt.wantSubject)
			}
			if ev.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", ev.URL, tt.wantURL)
			}
			if ev.Excerpt != tt.wantExcerpt {
				t.Errorf("Excerpt = %q, want %q", ev.Excerpt, tt.wantExcerpt)
			}
		})
	}
}

func TestParseCollectsUsersAndBots(t *testing.T) {
	ev, err := Parse("issue_comment", loadFixture(t, "bot_comment"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ev.Actor.IsBot {
		t.Error("dependabot[bot] should be detected as a bot")
	}
	want := map[int64]string{1: "alice", 49699333: "dependabot[bot]"}
	if !reflect.DeepEqual(ev.Users, want) {
		t.Errorf("Users = %v, want %v", ev.Users, want)
	}
	if ev.Name != "issue_comment" || ev.Action != "created" {
		t.Errorf("Name/Action = %q/%q", ev.Name, ev.Action)
	}
}

func TestParseRequestedUsersAreCollected(t *testing.T) {
	ev, err := Parse("pull_request", loadFixture(t, "pull_request_review_requested_user"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.Users[3] != "carol" || ev.Users[1] != "alice" {
		t.Errorf("Users = %v", ev.Users)
	}
}

func TestParseErrors(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":     `{`,
		"not an object":    `[1,2]`,
		"wrong field type": `{"action": 5}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("issues", []byte(body)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseUnknownEventWithoutTextIsEmpty(t *testing.T) {
	ev, err := Parse("star", []byte(`{"action":"created","sender":{"login":"bob","id":2,"type":"User"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ev.Targets) != 0 || !ev.Mentions.IsEmpty() || ev.Actor != bob {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestParseGenericEventUsesEventNamedObject(t *testing.T) {
	body := `{"action":"created","milestone":{"title":"v2","body":"owner @carol","html_url":"https://x/m/1","creator":{"login":"bob","id":2}},"sender":{"login":"bob","id":2,"type":"User"}}`

	ev, err := Parse("milestone", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(ev.Mentions.Users, []string{"carol"}) || ev.URL != "https://x/m/1" {
		t.Errorf("generic event: %+v", ev)
	}
}

func equalOrEmpty(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

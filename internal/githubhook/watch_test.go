package githubhook

import "testing"

func TestParseWatchActivity(t *testing.T) {
	tests := []struct {
		event, fixture string
		wantLevel      WatchLevel
		wantReason     string
	}{
		{"pull_request", "pull_request_opened", WatchNewPRs, "new PR"},
		{"pull_request", "pull_request_opened_draft", WatchNone, ""},
		{"pull_request", "pull_request_ready_for_review", WatchNewPRs, "new PR"},
		{"pull_request", "pull_request_closed_merged", WatchPRs, "PR merged"},
		{"pull_request", "pull_request_closed_unmerged", WatchPRs, "PR closed"},
		{"pull_request", "pull_request_closed_draft", WatchNone, ""},
		{"pull_request", "pull_request_reopened", WatchPRs, "PR reopened"},
		{"pull_request", "pull_request_synchronize", WatchNone, ""},
		{"pull_request", "pull_request_review_requested_user", WatchNone, ""},
		{"pull_request", "pull_request_edited_body", WatchNone, ""},
		{"pull_request_review", "pull_request_review_approved", WatchPRs, "new review"},
		{"pull_request_review", "pull_request_review_on_draft", WatchNone, ""},
		{"pull_request_review_comment", "pull_request_review_comment_created", WatchNone, ""},
		{"issue_comment", "issue_comment_created_pr", WatchAll, "new comment"},
		{"issue_comment", "issue_comment_edited", WatchNone, ""},
		{"issues", "issues_opened", WatchAll, "new issue"},
		{"issues", "issues_closed", WatchAll, "issue closed"},
		{"issues", "issues_reopened", WatchAll, "issue reopened"},
		{"issues", "issues_labeled", WatchNone, ""},
		{"discussion", "discussion_created", WatchAll, "new discussion"},
		{"discussion_comment", "discussion_comment_created", WatchAll, "new comment"},
		{"release", "release_published", WatchAll, "release published"},
		{"release", "release_created", WatchNone, ""},
		{"commit_comment", "commit_comment_created", WatchNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			ev, err := Parse(tt.event, loadFixture(t, tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Watch.Level != tt.wantLevel || ev.Watch.Reason != tt.wantReason {
				t.Fatalf("Watch = %+v, want {%v %q}", ev.Watch, tt.wantLevel, tt.wantReason)
			}
		})
	}
}

func TestWatchLevelNames(t *testing.T) {
	for _, l := range []WatchLevel{WatchNewPRs, WatchPRs, WatchAll} {
		got, ok := ParseWatchLevel(l.String())
		if !ok || got != l {
			t.Errorf("round trip %v: got %v, %v", l, got, ok)
		}
	}
	if _, ok := ParseWatchLevel("everything"); ok {
		t.Error("unknown level accepted")
	}
	if WatchNone.String() != "" || WatchNewPRs >= WatchPRs || WatchPRs >= WatchAll {
		t.Error("levels must be ordered new-prs < prs < all")
	}
}

package githubhook

// WatchLevel is how much activity a watcher wants, from least to most.
// Higher levels include everything below them.
type WatchLevel int

// Watch levels. WatchNone marks events no watcher is pinged for.
const (
	WatchNone WatchLevel = iota
	WatchNewPRs
	WatchPRs
	WatchAll
)

var watchLevelNames = map[WatchLevel]string{
	WatchNewPRs: "new-prs",
	WatchPRs:    "prs",
	WatchAll:    "all",
}

// String returns the level's name as stored and shown ("new-prs", "prs", "all").
func (l WatchLevel) String() string { return watchLevelNames[l] }

// ParseWatchLevel parses a level name.
func ParseWatchLevel(s string) (WatchLevel, bool) {
	for l, name := range watchLevelNames {
		if name == s {
			return l, true
		}
	}
	return WatchNone, false
}

// WatchActivity says which watchers an event concerns: those watching at
// Level or above, pinged with Reason.
type WatchActivity struct {
	Level  WatchLevel
	Reason string
}

// watchActivity classifies an event for watchers. Draft PRs are skipped
// until they are marked ready for review; pushes, edits, labels and inline
// review comments never notify watchers.
func watchActivity(name string, p *ghPayload) WatchActivity {
	if p.PullRequest != nil && p.PullRequest.Draft {
		return WatchActivity{}
	}
	at := func(l WatchLevel, reason string) WatchActivity { return WatchActivity{Level: l, Reason: reason} }

	switch name + "." + p.Action {
	case "pull_request.opened", "pull_request.ready_for_review":
		return at(WatchNewPRs, "new PR")
	case "pull_request.closed":
		if p.PullRequest != nil && p.PullRequest.Merged {
			return at(WatchPRs, "PR merged")
		}
		return at(WatchPRs, "PR closed")
	case "pull_request.reopened":
		return at(WatchPRs, "PR reopened")
	case "pull_request_review.submitted":
		return at(WatchPRs, "new review")
	case "issue_comment.created", "discussion_comment.created":
		return at(WatchAll, "new comment")
	case "issues.opened":
		return at(WatchAll, "new issue")
	case "issues.closed":
		return at(WatchAll, "issue closed")
	case "issues.reopened":
		return at(WatchAll, "issue reopened")
	case "discussion.created":
		return at(WatchAll, "new discussion")
	case "release.published":
		return at(WatchAll, "release published")
	}
	return WatchActivity{}
}

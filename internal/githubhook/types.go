// Package githubhook verifies GitHub webhook deliveries and turns their
// payloads into a normalized Event describing who should be notified and why.
package githubhook

import (
	"strings"

	"github.com/benpsk/pingcord/internal/mention"
)

// User is a GitHub account as it appears in a payload.
type User struct {
	ID    int64
	Login string
	IsBot bool
}

// Kind is the type of thing an event is about.
type Kind string

// Subject kinds.
const (
	KindPR         Kind = "pull request"
	KindIssue      Kind = "issue"
	KindDiscussion Kind = "discussion"
	KindCommit     Kind = "commit"
	KindRelease    Kind = "release"
)

// Subject is the PR, issue, discussion, commit or release an event is about.
type Subject struct {
	Kind   Kind
	Number int
	Title  string
	URL    string
	Author User
}

// Reason explains why a recipient is pinged.
type Reason string

// Reasons, worded to be shown next to the pinged user.
const (
	ReasonMentioned           Reason = "mentioned"
	ReasonReviewRequested     Reason = "review requested"
	ReasonAssigned            Reason = "assigned"
	ReasonApproved            Reason = "approved your PR"
	ReasonChangesRequested    Reason = "requested changes on your PR"
	ReasonCommentOnPR         Reason = "commented on your PR"
	ReasonCommentOnIssue      Reason = "commented on your issue"
	ReasonCommentOnDiscussion Reason = "commented on your discussion"
)

// Target is a recipient chosen by role (reviewer, assignee, author). Exactly
// one of User or Team ("org/slug") is set.
type Target struct {
	User   User
	Team   string
	Reason Reason
}

// Event is a normalized GitHub webhook delivery.
type Event struct {
	Name     string // X-GitHub-Event, e.g. "pull_request"
	Action   string // payload "action", e.g. "opened"
	Repo     string // "owner/name"
	Org      string // organization login (or repository owner)
	Actor    User   // who triggered the event
	Subject  Subject
	URL      string // most specific link (comment/review), else the subject
	Excerpt  string // the new text that triggered the event, if any
	Targets  []Target
	Mentions mention.Result   // @mentions in the new text (only new ones on edits)
	Users    map[int64]string // every id → login seen, for rename tracking
	Watch    WatchActivity    // which watchers this event concerns
}

// ghUser mirrors GitHub's user object.
type ghUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (u *ghUser) toUser() User {
	if u == nil {
		return User{}
	}
	return User{
		ID:    u.ID,
		Login: u.Login,
		IsBot: u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]"),
	}
}

// ghObject covers the fields pingcord reads from issues, PRs, comments,
// reviews, discussions, releases and similar objects.
type ghObject struct {
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	Name        string  `json:"name"`
	TagName     string  `json:"tag_name"`
	Body        *string `json:"body"`
	HTMLURL     string  `json:"html_url"`
	State       string  `json:"state"`
	CommitID    string  `json:"commit_id"`
	Draft       bool    `json:"draft"`
	Merged      bool    `json:"merged"`
	User        *ghUser `json:"user"`
	Author      *ghUser `json:"author"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

func (o *ghObject) body() string {
	if o == nil || o.Body == nil {
		return ""
	}
	return *o.Body
}

func (o *ghObject) author() *ghUser {
	if o.User != nil {
		return o.User
	}
	return o.Author
}

func (o *ghObject) title() string {
	for _, t := range []string{o.Title, o.Name, o.TagName} {
		if t != "" {
			return t
		}
	}
	return ""
}

type ghPayload struct {
	Action     string  `json:"action"`
	Sender     *ghUser `json:"sender"`
	Repository *struct {
		FullName string  `json:"full_name"`
		Owner    *ghUser `json:"owner"`
	} `json:"repository"`
	Organization *struct {
		Login string `json:"login"`
	} `json:"organization"`
	PullRequest       *ghObject `json:"pull_request"`
	Issue             *ghObject `json:"issue"`
	Discussion        *ghObject `json:"discussion"`
	Release           *ghObject `json:"release"`
	Comment           *ghObject `json:"comment"`
	Review            *ghObject `json:"review"`
	Assignee          *ghUser   `json:"assignee"`
	RequestedReviewer *ghUser   `json:"requested_reviewer"`
	RequestedTeam     *struct {
		Slug string `json:"slug"`
	} `json:"requested_team"`
	Changes *struct {
		Body *struct {
			From string `json:"from"`
		} `json:"body"`
	} `json:"changes"`
}

func (p *ghPayload) org() string {
	if p.Organization != nil && p.Organization.Login != "" {
		return p.Organization.Login
	}
	if p.Repository != nil && p.Repository.Owner != nil {
		return p.Repository.Owner.Login
	}
	return ""
}

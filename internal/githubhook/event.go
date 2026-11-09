package githubhook

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benpsk/pingcord/internal/mention"
)

const shortSHALen = 7

// mentionActions are actions whose new text may notify @mentioned users.
// "edited" is handled separately so only newly added mentions count.
var mentionActions = map[string]bool{
	"created":   true,
	"opened":    true,
	"submitted": true,
	"published": true,
}

// skipMentionActions overrides mentionActions per event. GitHub sends
// release.created for drafts and alongside release.published for a direct
// publish, so only "published" may ping.
var skipMentionActions = map[string]bool{
	"release.created": true,
}

// Parse turns a webhook delivery into an Event. name is the X-GitHub-Event
// header. Events that should notify nobody return an Event with no Targets
// and no Mentions.
func Parse(name string, body []byte) (Event, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, fmt.Errorf("decode %s payload: %w", name, err)
	}
	var p ghPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{}, fmt.Errorf("decode %s payload: %w", name, err)
	}

	primary, err := primaryObject(name, &p, raw)
	if err != nil {
		return Event{}, err
	}

	ev := Event{
		Name:    name,
		Action:  p.Action,
		Actor:   p.Sender.toUser(),
		Subject: subjectOf(name, &p, primary),
	}
	if p.Repository != nil {
		ev.Repo = p.Repository.FullName
	}
	ev.Org = p.org()
	ev.URL = ev.Subject.URL
	if primary != nil && primary.HTMLURL != "" {
		ev.URL = primary.HTMLURL
	}

	if !skipMentionActions[name+"."+p.Action] {
		ev.Mentions, ev.Excerpt = textMentions(&p, primary)
	}
	ev.Targets = roleTargets(name, &p, ev.Subject)
	ev.Watch = watchActivity(name, &p)
	ev.Users = collectUsers(&p, primary)
	return ev, nil
}

// primaryObject is the object that was just created or changed: a comment or
// review if present, otherwise the object named after the event
// ("issues" → "issue", "milestone" → "milestone").
func primaryObject(name string, p *ghPayload, raw map[string]json.RawMessage) (*ghObject, error) {
	if p.Comment != nil {
		return p.Comment, nil
	}
	if p.Review != nil {
		return p.Review, nil
	}
	for _, key := range []string{name, strings.TrimSuffix(name, "s")} {
		msg, ok := raw[key]
		if !ok || len(msg) == 0 || msg[0] != '{' {
			continue
		}
		var obj ghObject
		if err := json.Unmarshal(msg, &obj); err != nil {
			return nil, fmt.Errorf("decode %s.%s: %w", name, key, err)
		}
		return &obj, nil
	}
	return nil, nil
}

func subjectOf(name string, p *ghPayload, primary *ghObject) Subject {
	switch {
	case p.PullRequest != nil:
		return objectSubject(KindPR, p.PullRequest)
	case p.Issue != nil:
		kind := KindIssue
		if p.Issue.PullRequest != nil {
			kind = KindPR
		}
		return objectSubject(kind, p.Issue)
	case p.Discussion != nil:
		return objectSubject(KindDiscussion, p.Discussion)
	case p.Release != nil:
		return objectSubject(KindRelease, p.Release)
	case name == "commit_comment" && p.Comment != nil:
		sha := p.Comment.CommitID
		if len(sha) > shortSHALen {
			sha = sha[:shortSHALen]
		}
		return Subject{Kind: KindCommit, Title: sha, URL: p.Comment.HTMLURL}
	case primary != nil:
		return objectSubject(Kind(strings.ReplaceAll(name, "_", " ")), primary)
	}
	return Subject{}
}

func objectSubject(kind Kind, o *ghObject) Subject {
	return Subject{
		Kind:   kind,
		Number: o.Number,
		Title:  o.title(),
		URL:    o.HTMLURL,
		Author: o.author().toUser(),
	}
}

// textMentions returns mentions in the primary object's new text and the
// text itself. On edits only mentions added by the edit are returned.
func textMentions(p *ghPayload, primary *ghObject) (mention.Result, string) {
	if primary == nil {
		return mention.Result{}, ""
	}
	text := primary.body()
	switch {
	case mentionActions[p.Action]:
		return mention.Extract(text), text
	case p.Action == "edited" && p.Changes != nil && p.Changes.Body != nil:
		return mention.Added(p.Changes.Body.From, text), text
	}
	return mention.Result{}, ""
}

// roleTargets returns recipients who are notified because of their role
// rather than an @mention.
func roleTargets(name string, p *ghPayload, subject Subject) []Target {
	switch name + "." + p.Action {
	case "pull_request.review_requested":
		if p.RequestedReviewer != nil {
			return []Target{{User: p.RequestedReviewer.toUser(), Reason: ReasonReviewRequested}}
		}
		if p.RequestedTeam != nil && p.RequestedTeam.Slug != "" {
			return []Target{{Team: p.org() + "/" + p.RequestedTeam.Slug, Reason: ReasonReviewRequested}}
		}
	case "pull_request.assigned", "issues.assigned":
		if p.Assignee != nil {
			return []Target{{User: p.Assignee.toUser(), Reason: ReasonAssigned}}
		}
	case "pull_request_review.submitted":
		if reason, ok := reviewReason(p.Review); ok && subject.Author.ID != 0 {
			return []Target{{User: subject.Author, Reason: reason}}
		}
	case "issue_comment.created":
		reason := ReasonCommentOnIssue
		if subject.Kind == KindPR {
			reason = ReasonCommentOnPR
		}
		return authorTarget(subject, reason)
	case "discussion_comment.created":
		return authorTarget(subject, ReasonCommentOnDiscussion)
	}
	return nil
}

func authorTarget(subject Subject, reason Reason) []Target {
	if subject.Author.ID == 0 {
		return nil
	}
	return []Target{{User: subject.Author, Reason: reason}}
}

func reviewReason(review *ghObject) (Reason, bool) {
	if review == nil {
		return "", false
	}
	switch strings.ToLower(review.State) {
	case "approved":
		return ReasonApproved, true
	case "changes_requested":
		return ReasonChangesRequested, true
	case "commented":
		return ReasonCommentOnPR, true
	}
	return "", false
}

// collectUsers gathers every (id, login) pair in the payload so renamed
// users can be refreshed in the store.
func collectUsers(p *ghPayload, primary *ghObject) map[int64]string {
	out := map[int64]string{}
	add := func(u *ghUser) {
		if u != nil && u.ID > 0 && u.Login != "" {
			out[u.ID] = u.Login
		}
	}
	add(p.Sender)
	add(p.Assignee)
	add(p.RequestedReviewer)
	for _, o := range []*ghObject{p.PullRequest, p.Issue, p.Discussion, p.Release, primary} {
		if o != nil {
			add(o.User)
			add(o.Author)
		}
	}
	return out
}

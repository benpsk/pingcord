package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/githubhook"
	"github.com/benpsk/pingcord/internal/mention"
	"github.com/benpsk/pingcord/internal/store"
)

// --- fakes -----------------------------------------------------------------

type fakeStore struct {
	links      map[int64]string // github id → discord id
	ignored    map[int64]bool
	refreshed  map[int64]string
	linkErr    error
	ignoredErr error
	refreshErr error
}

func (f *fakeStore) RefreshLogins(_ context.Context, logins map[int64]string) error {
	f.refreshed = logins
	return f.refreshErr
}

func (f *fakeStore) LinksByGitHubIDs(_ context.Context, ids []int64) (map[int64]store.Link, error) {
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	out := map[int64]store.Link{}
	for _, id := range ids {
		if d, ok := f.links[id]; ok {
			out[id] = store.Link{GitHubID: id, DiscordID: d}
		}
	}
	return out, nil
}

func (f *fakeStore) IgnoredIDs(_ context.Context, ids []int64) (map[int64]bool, error) {
	if f.ignoredErr != nil {
		return nil, f.ignoredErr
	}
	out := map[int64]bool{}
	for _, id := range ids {
		if f.ignored[id] {
			out[id] = true
		}
	}
	return out, nil
}

type fakeDir struct {
	ids        map[string]int64
	teams      map[string][]githubapi.User
	resolveErr error
}

func (f *fakeDir) ResolveLogins(_ context.Context, logins []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, l := range logins {
		if id, ok := f.ids[l]; ok {
			out[l] = id
		}
	}
	return out, f.resolveErr
}

func (f *fakeDir) TeamMembers(_ context.Context, team string) ([]githubapi.User, error) {
	members, ok := f.teams[team]
	if !ok {
		return nil, errors.New("team not found")
	}
	return members, nil
}

type fakeSender struct {
	sent []discord.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, msg discord.Message) error {
	f.sent = append(f.sent, msg)
	return f.err
}

// --- fixtures ----------------------------------------------------------------

var (
	alice = githubhook.User{ID: 1, Login: "alice"}
	bob   = githubhook.User{ID: 2, Login: "bob"}
	carol = githubhook.User{ID: 3, Login: "carol"}
)

func setup() (*Notifier, *fakeStore, *fakeDir, *fakeSender) {
	st := &fakeStore{
		links:   map[int64]string{1: "100", 2: "200", 3: "300", 4: "400", 6: "600"},
		ignored: map[int64]bool{},
	}
	dir := &fakeDir{
		ids: map[string]int64{"alice": 1, "bob": 2, "carol": 3, "dave": 4, "erin": 5, "renovate[bot]": 6},
		teams: map[string][]githubapi.User{
			"my-org/qa": {{ID: 2, Login: "bob"}, {ID: 4, Login: "dave"}},
		},
	}
	snd := &fakeSender{}
	n := New(st, dir, snd,
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithClock(func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }),
	)
	return n, st, dir, snd
}

func prComment() githubhook.Event {
	return githubhook.Event{
		Name: "issue_comment", Action: "created", Repo: "my-org/api", Actor: bob,
		Subject:  githubhook.Subject{Kind: githubhook.KindPR, Number: 12, Title: "Add login flow", URL: "https://github.com/my-org/api/pull/12", Author: alice},
		URL:      "https://github.com/my-org/api/pull/12#issuecomment-1",
		Excerpt:  "LGTM @carol, cc @my-org/qa",
		Targets:  []githubhook.Target{{User: alice, Reason: githubhook.ReasonCommentOnPR}},
		Mentions: mention.Result{Users: []string{"carol"}, Teams: []string{"my-org/qa"}},
		Users:    map[int64]string{1: "alice", 2: "bob"},
	}
}

// --- tests -------------------------------------------------------------------

func TestHandlePingsEveryoneConcernedExceptActor(t *testing.T) {
	n, st, _, snd := setup()

	res, err := n.Handle(context.Background(), prComment())

	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(snd.sent))
	}
	msg := snd.sent[0]
	wantContent := "<@100> commented on your PR\n<@300> mentioned\n<@400> mentioned via @\u200bmy-org/qa"
	if msg.Content != wantContent {
		t.Errorf("Content =\n%s\nwant\n%s", msg.Content, wantContent)
	}
	if want := []string{"100", "300", "400"}; !reflect.DeepEqual(msg.AllowedMentions.Users, want) {
		t.Errorf("allowed users = %v, want %v", msg.AllowedMentions.Users, want)
	}
	if msg.AllowedMentions.Parse == nil || len(msg.AllowedMentions.Parse) != 0 {
		t.Errorf("Parse = %#v, want empty non-nil", msg.AllowedMentions.Parse)
	}
	if !reflect.DeepEqual(res.Pinged, []string{"100", "300", "400"}) {
		t.Errorf("res.Pinged = %v", res.Pinged)
	}
	if !reflect.DeepEqual(st.refreshed, map[int64]string{1: "alice", 2: "bob"}) {
		t.Errorf("logins not refreshed: %v", st.refreshed)
	}
}

func TestHandleGroupsReasonsPerPerson(t *testing.T) {
	n, _, _, snd := setup()
	ev := prComment()
	ev.Targets = []githubhook.Target{{User: carol, Reason: githubhook.ReasonReviewRequested}}
	ev.Mentions = mention.Result{Users: []string{"carol", "carol"}}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if got := snd.sent[0].Content; got != "<@300> review requested, mentioned" {
		t.Errorf("Content = %q", got)
	}
}

func TestHandleTeamReviewRequest(t *testing.T) {
	n, _, _, snd := setup()
	ev := prComment()
	ev.Actor = alice
	ev.Targets = []githubhook.Target{{Team: "my-org/qa", Reason: githubhook.ReasonReviewRequested}}
	ev.Mentions = mention.Result{}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	want := "<@200> review requested via @\u200bmy-org/qa\n<@400> review requested via @\u200bmy-org/qa"
	if got := snd.sent[0].Content; got != want {
		t.Errorf("Content = %q, want %q", got, want)
	}
}

func TestHandleSkipsIgnoredUnlinkedAndBots(t *testing.T) {
	n, st, _, snd := setup()
	st.ignored[3] = true // carol ignored
	ev := prComment()
	ev.Targets = []githubhook.Target{
		{User: githubhook.User{ID: 5, Login: "erin"}, Reason: githubhook.ReasonAssigned},                       // unlinked
		{User: githubhook.User{ID: 6, Login: "renovate[bot]", IsBot: true}, Reason: githubhook.ReasonAssigned}, // bot
	}
	ev.Mentions = mention.Result{Users: []string{"carol", "renovate[bot]"}}

	res, err := n.Handle(context.Background(), ev)

	if err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatalf("nothing should be sent, got %+v", snd.sent)
	}
	if !reflect.DeepEqual(res.Unlinked, []string{"erin"}) {
		t.Errorf("Unlinked = %v", res.Unlinked)
	}
}

func TestHandleBotRecipientsAllowedWhenIgnoreBotsOff(t *testing.T) {
	n, _, _, snd := setup()
	n.ignoreBots = false
	ev := prComment()
	ev.Targets = []githubhook.Target{{User: githubhook.User{ID: 6, Login: "renovate[bot]", IsBot: true}, Reason: githubhook.ReasonAssigned}}
	ev.Mentions = mention.Result{}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 {
		t.Fatal("bot recipient should be pinged when IgnoreBots is off")
	}
}

func TestHandleIgnoresBotActors(t *testing.T) {
	n, _, _, snd := setup()
	ev := prComment()
	ev.Actor = githubhook.User{ID: 49699333, Login: "dependabot[bot]", IsBot: true}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatal("events from bots should be ignored")
	}
}

func TestHandleDegradesGracefullyOnLookupErrors(t *testing.T) {
	n, st, dir, snd := setup()
	st.refreshErr = errors.New("db locked")
	dir.resolveErr = errors.New("github down")
	ev := prComment()
	ev.Mentions.Teams = []string{"my-org/unknown"}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle should not fail on lookup errors: %v", err)
	}
	if len(snd.sent) != 1 || !strings.Contains(snd.sent[0].Content, "<@300> mentioned") {
		t.Fatalf("partial results should still be sent: %+v", snd.sent)
	}
}

func TestHandleReturnsStoreAndSendErrors(t *testing.T) {
	ctx := context.Background()

	n, st, _, _ := setup()
	st.linkErr = errors.New("db gone")
	if _, err := n.Handle(ctx, prComment()); err == nil {
		t.Error("link lookup error should be returned")
	}

	n, st, _, _ = setup()
	st.ignoredErr = errors.New("db gone")
	if _, err := n.Handle(ctx, prComment()); err == nil {
		t.Error("ignored lookup error should be returned")
	}

	n, _, _, snd := setup()
	snd.err = errors.New("discord down")
	if _, err := n.Handle(ctx, prComment()); err == nil {
		t.Error("send error should be returned")
	}
}

func TestHandleNoRecipientsSendsNothing(t *testing.T) {
	n, _, _, snd := setup()
	ev := githubhook.Event{Name: "star", Action: "created", Actor: bob}

	res, err := n.Handle(context.Background(), ev)

	if err != nil || len(snd.sent) != 0 || len(res.Pinged) != 0 {
		t.Fatalf("res=%+v err=%v sent=%d", res, err, len(snd.sent))
	}
}

func TestHandleFailsWhenRequestedTeamCannotBeResolved(t *testing.T) {
	n, _, _, snd := setup()
	ev := prComment()
	ev.Targets = []githubhook.Target{{Team: "my-org/unknown", Reason: githubhook.ReasonReviewRequested}}

	_, err := n.Handle(context.Background(), ev)

	if err == nil {
		t.Fatal("a review request whose team can't be resolved must fail the delivery so it can be redelivered")
	}
	if len(snd.sent) != 0 {
		t.Fatal("nothing should be sent on failure")
	}
}

func TestHandleCapsMentionLookups(t *testing.T) {
	n, _, dir, _ := setup()
	var asked []string
	counting := &countingDir{fakeDir: dir, asked: &asked}
	n.dir = counting
	ev := prComment()
	ev.Mentions.Users = nil
	for i := range maxMentionLookups + 50 {
		ev.Mentions.Users = append(ev.Mentions.Users, fmt.Sprintf("user%d", i))
	}
	for i := range maxTeamMentions + 3 {
		ev.Mentions.Teams = append(ev.Mentions.Teams, fmt.Sprintf("my-org/t%d", i))
	}

	if _, err := n.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(asked) != maxMentionLookups {
		t.Fatalf("resolved %d logins, want cap %d", len(asked), maxMentionLookups)
	}
	if counting.teamCalls != maxTeamMentions {
		t.Fatalf("team lookups = %d, want cap %d", counting.teamCalls, maxTeamMentions)
	}
}

type countingDir struct {
	*fakeDir
	asked     *[]string
	teamCalls int
}

func (c *countingDir) ResolveLogins(ctx context.Context, logins []string) (map[string]int64, error) {
	*c.asked = append(*c.asked, logins...)
	return c.fakeDir.ResolveLogins(ctx, logins)
}

func (c *countingDir) TeamMembers(ctx context.Context, team string) ([]githubapi.User, error) {
	c.teamCalls++
	return c.fakeDir.TeamMembers(ctx, team)
}

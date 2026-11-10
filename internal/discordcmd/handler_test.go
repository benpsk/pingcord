package discordcmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benpsk/pingcord/internal/discord"
	"github.com/benpsk/pingcord/internal/githubapi"
	"github.com/benpsk/pingcord/internal/store"
)

const (
	adminID = "323456789012345678"
	guildID = "223456789012345678"
	userID  = "423456789012345678"
)

// --- fakes -----------------------------------------------------------------

type fakeStore struct {
	links   []store.Link
	ignored []store.Ignored
	watches []store.Watch
	repos   []string
	err     error
}

func (f *fakeStore) SetWatch(_ context.Context, w store.Watch) error {
	if f.err != nil {
		return f.err
	}
	for i, old := range f.watches {
		if old.DiscordID == w.DiscordID && strings.EqualFold(old.Repo, w.Repo) {
			f.watches[i] = w
			return nil
		}
	}
	f.watches = append(f.watches, w)
	return nil
}

func (f *fakeStore) RemoveWatch(_ context.Context, discordID, repo string) (store.Watch, error) {
	for i, w := range f.watches {
		if w.DiscordID == discordID && strings.EqualFold(w.Repo, repo) {
			f.watches = append(f.watches[:i], f.watches[i+1:]...)
			return w, nil
		}
	}
	return store.Watch{}, store.ErrNotFound
}

func (f *fakeStore) ListWatches(context.Context) ([]store.Watch, error) { return f.watches, f.err }

func (f *fakeStore) ListRepos(context.Context) ([]string, error) { return f.repos, f.err }

func (f *fakeStore) UpsertLink(_ context.Context, l store.Link) ([]store.Link, error) {
	if f.err != nil {
		return nil, f.err
	}
	var kept, replaced []store.Link
	for _, old := range f.links {
		if (old.DiscordID == l.DiscordID) != (old.GitHubID == l.GitHubID) {
			replaced = append(replaced, old)
			continue
		}
		if old.DiscordID != l.DiscordID {
			kept = append(kept, old)
		}
	}
	f.links = append(kept, l)
	return replaced, nil
}

func (f *fakeStore) DeleteLinkByDiscordID(_ context.Context, id string) (store.Link, error) {
	for i, l := range f.links {
		if l.DiscordID == id {
			f.links = append(f.links[:i], f.links[i+1:]...)
			return l, nil
		}
	}
	return store.Link{}, store.ErrNotFound
}

func (f *fakeStore) ListLinks(context.Context) ([]store.Link, error) { return f.links, f.err }

func (f *fakeStore) Ignore(_ context.Context, id int64, login, by string) error {
	f.ignored = append(f.ignored, store.Ignored{GitHubID: id, GitHubLogin: login, CreatedBy: by})
	return f.err
}

func (f *fakeStore) Unignore(_ context.Context, login string) (store.Ignored, error) {
	for i, ig := range f.ignored {
		if strings.EqualFold(ig.GitHubLogin, login) {
			f.ignored = append(f.ignored[:i], f.ignored[i+1:]...)
			return ig, nil
		}
	}
	return store.Ignored{}, store.ErrNotFound
}

func (f *fakeStore) ListIgnored(context.Context) ([]store.Ignored, error) { return f.ignored, f.err }

type fakeDir struct {
	users   map[string]githubapi.User
	members []githubapi.User
	repos   []string
	repoErr error
	err     error
}

func (f *fakeDir) LookupUser(_ context.Context, login string) (githubapi.User, error) {
	if f.err != nil {
		return githubapi.User{}, f.err
	}
	u, ok := f.users[strings.ToLower(login)]
	if !ok {
		return githubapi.User{}, githubapi.ErrNotFound
	}
	return u, nil
}

func (f *fakeDir) OrgMembers(context.Context) ([]githubapi.User, error) { return f.members, f.err }

func (f *fakeDir) OrgRepos(context.Context) ([]string, error) { return f.repos, f.repoErr }

type fakeAnnouncer struct {
	mu    sync.Mutex
	sent  []discord.Message
	delay time.Duration
}

func (f *fakeAnnouncer) Send(ctx context.Context, m discord.Message) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

// --- harness -----------------------------------------------------------------

type harness struct {
	h     *Handler
	priv  ed25519.PrivateKey
	store *fakeStore
	dir   *fakeDir
	ann   *fakeAnnouncer
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hs := &harness{
		priv:  priv,
		store: &fakeStore{},
		dir: &fakeDir{
			users: map[string]githubapi.User{"alice": {ID: 1, Login: "Alice"}, "bob": {ID: 2, Login: "bob"}, "dependabot": {ID: 9, Login: "dependabot"}},
			members: []githubapi.User{
				{ID: 1, Login: "Alice"}, {ID: 2, Login: "bob"}, {ID: 3, Login: "carol"}, {ID: 4, Login: "malice"},
			},
		},
		ann: &fakeAnnouncer{},
		now: time.Unix(1_800_000_000, 0),
	}
	hs.h = NewHandler(Config{
		PublicKey: pub,
		GuildID:   guildID,
		Org:       "my-org",
		IsAdmin:   func(id string) bool { return id == adminID },
	}, hs.store, hs.dir, hs.ann,
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithClock(func() time.Time { return hs.now }),
	)
	return hs
}

type response struct {
	Type int `json:"type"`
	Data struct {
		Content         string                   `json:"content"`
		Flags           int                      `json:"flags"`
		AllowedMentions *discord.AllowedMentions `json:"allowed_mentions"`
		Choices         []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"choices"`
	} `json:"data"`
}

func (hs *harness) post(t *testing.T, body string) (int, response) {
	t.Helper()
	ts := strconv.FormatInt(hs.now.Unix(), 10)
	sig := ed25519.Sign(hs.priv, []byte(ts+body))
	req := httptest.NewRequest(http.MethodPost, "/discord/interactions", strings.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()

	hs.h.ServeHTTP(rec, req)
	hs.h.Wait() // let background audit announcements finish

	var resp response
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q", ct)
		}
	}
	return rec.Code, resp
}

// command builds an /admin <sub> interaction from the given caller.
func command(typ int, caller, guild, sub string, opts ...string) string {
	var options []string
	for i := 0; i+1 < len(opts); i += 2 {
		name, value := opts[i], opts[i+1]
		focused := ""
		if strings.HasSuffix(name, "*") {
			name, focused = strings.TrimSuffix(name, "*"), `,"focused":true`
		}
		optType := 3
		if name == "user" {
			optType = 6
		}
		options = append(options, fmt.Sprintf(`{"name":%q,"type":%d,"value":%q%s}`, name, optType, value, focused))
	}
	return fmt.Sprintf(`{"type":%d,"id":"1","token":"t","guild_id":%q,"member":{"user":{"id":%q,"username":"admin"}},
		"data":{"name":"admin","type":1,"options":[{"name":%q,"type":1,"options":[%s]}],
		"resolved":{"users":{%q:{"id":%q,"username":"dev"},"999999999999999999":{"id":"999999999999999999","username":"somebot","bot":true}}}}}`,
		typ, guild, caller, sub, strings.Join(options, ","), userID, userID)
}

func adminCmd(sub string, opts ...string) string {
	return command(typeApplicationCommand, adminID, guildID, sub, opts...)
}

func expectReply(t *testing.T, code int, resp response, wantContains ...string) {
	t.Helper()
	if code != http.StatusOK || resp.Type != responseChannelMessage {
		t.Fatalf("code=%d type=%d", code, resp.Type)
	}
	if resp.Data.Flags != flagEphemeral {
		t.Errorf("reply should be ephemeral, flags=%d", resp.Data.Flags)
	}
	if resp.Data.AllowedMentions == nil || len(resp.Data.AllowedMentions.Parse) != 0 || len(resp.Data.AllowedMentions.Users) != 0 {
		t.Errorf("reply must not ping anyone: %+v", resp.Data.AllowedMentions)
	}
	for _, w := range wantContains {
		if !strings.Contains(resp.Data.Content, w) {
			t.Errorf("content %q does not contain %q", resp.Data.Content, w)
		}
	}
}

// --- security ----------------------------------------------------------------

func TestPing(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, `{"type":1}`)

	if code != http.StatusOK || resp.Type != responsePong {
		t.Fatalf("code=%d type=%d", code, resp.Type)
	}
}

func TestSignatureRejected(t *testing.T) {
	hs := newHarness(t)
	body := `{"type":1}`
	ts := strconv.FormatInt(hs.now.Unix(), 10)
	valid := hex.EncodeToString(ed25519.Sign(hs.priv, []byte(ts+body)))

	tests := []struct {
		name, sig, ts, body string
	}{
		{"missing signature", "", ts, body},
		{"not hex", "zz", ts, body},
		{"wrong length", "abcd", ts, body},
		{"tampered body", valid, ts, `{"type":2}`},
		{"stale timestamp", hex.EncodeToString(ed25519.Sign(hs.priv, []byte("1000"+body))), "1000", body},
		{"non-numeric timestamp", hex.EncodeToString(ed25519.Sign(hs.priv, []byte("abc"+body))), "abc", body},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("X-Signature-Ed25519", tt.sig)
			req.Header.Set("X-Signature-Timestamp", tt.ts)
			rec := httptest.NewRecorder()

			hs.h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d", rec.Code)
			}
		})
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	hs := newHarness(t)
	body := `{"type":1,"pad":"` + strings.Repeat("a", maxBody) + `"}`

	code, _ := hs.post(t, body)

	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d", code)
	}
}

func TestMalformedInteraction(t *testing.T) {
	hs := newHarness(t)

	if code, _ := hs.post(t, `{"type":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("code = %d", code)
	}
	if code, _ := hs.post(t, `{"type":99}`); code != http.StatusBadRequest {
		t.Fatalf("unknown type code = %d", code)
	}
}

func TestNonAdminIsRefused(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, command(typeApplicationCommand, userID, guildID, "list"))

	expectReply(t, code, resp, "Only pingcord admins")
}

func TestWrongGuildIsRefused(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, command(typeApplicationCommand, adminID, "999999999999999999", "list"))

	expectReply(t, code, resp, "Only pingcord admins")
}

func TestDirectMessageIsRefused(t *testing.T) {
	hs := newHarness(t)
	body := fmt.Sprintf(`{"type":2,"user":{"id":%q},"data":{"name":"admin","options":[{"name":"list","type":1}]}}`, adminID)

	code, resp := hs.post(t, body)

	expectReply(t, code, resp, "Only pingcord admins")
}

func TestNonAdminAutocompleteIsEmpty(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, command(typeAutocomplete, userID, guildID, "link", "user", userID, "github*", "a"))

	if code != http.StatusOK || resp.Type != responseAutocomplete || len(resp.Data.Choices) != 0 {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

// --- commands ----------------------------------------------------------------

func TestLink(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, adminCmd("link", "user", userID, "github", "ALICE"))

	expectReply(t, code, resp, "Linked", "`Alice`", "<@"+userID+">")
	if len(hs.store.links) != 1 {
		t.Fatalf("links = %+v", hs.store.links)
	}
	l := hs.store.links[0]
	if l.GitHubID != 1 || l.GitHubLogin != "Alice" || l.DiscordID != userID || l.CreatedBy != adminID {
		t.Fatalf("stored link = %+v", l)
	}
	if len(hs.ann.sent) != 1 || !strings.Contains(hs.ann.sent[0].Content, "Alice") {
		t.Fatalf("announcement = %+v", hs.ann.sent)
	}
	am := hs.ann.sent[0].AllowedMentions
	if am.Parse == nil || len(am.Parse) != 0 || len(am.Users) != 0 {
		t.Fatalf("announcement must not ping: %+v", am)
	}
}

func TestLinkErrors(t *testing.T) {
	tests := []struct {
		name  string
		opts  []string
		setup func(*harness)
		want  string
	}{
		{name: "unknown github user", opts: []string{"user", userID, "github", "ghost"}, want: "not found"},
		{name: "invalid github login", opts: []string{"user", userID, "github", "bad login!"}, want: "isn't a valid GitHub username"},
		{name: "discord bot", opts: []string{"user", "999999999999999999", "github", "alice"}, want: "bot"},
		{name: "missing option", opts: []string{"user", userID}, want: "Missing"},
		{name: "github api down", opts: []string{"user", userID, "github", "alice"}, setup: func(h *harness) { h.dir.err = errors.New("timeout") }, want: "Something went wrong"},
		{name: "store down", opts: []string{"user", userID, "github", "alice"}, setup: func(h *harness) { h.store.err = errors.New("disk full") }, want: "Something went wrong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := newHarness(t)
			if tt.setup != nil {
				tt.setup(hs)
			}

			code, resp := hs.post(t, adminCmd("link", tt.opts...))

			expectReply(t, code, resp, tt.want)
			if len(hs.ann.sent) != 0 {
				t.Errorf("failed link should not be announced")
			}
		})
	}
}

func TestUnlink(t *testing.T) {
	hs := newHarness(t)
	hs.store.links = []store.Link{{GitHubID: 1, GitHubLogin: "Alice", DiscordID: userID}}

	code, resp := hs.post(t, adminCmd("unlink", "user", userID))
	expectReply(t, code, resp, "Unlinked", "`Alice`")
	if len(hs.store.links) != 0 || len(hs.ann.sent) != 1 {
		t.Fatalf("links=%v announcements=%d", hs.store.links, len(hs.ann.sent))
	}

	code, resp = hs.post(t, adminCmd("unlink", "user", userID))
	expectReply(t, code, resp, "isn't linked")
}

func TestList(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, adminCmd("list"))
	expectReply(t, code, resp, "No links yet")

	hs.store.links = []store.Link{{GitHubID: 1, GitHubLogin: "Alice", DiscordID: userID}}
	code, resp = hs.post(t, adminCmd("list"))
	expectReply(t, code, resp, "Links (1)", "`Alice` → <@"+userID+">")
}

func TestListIsTruncatedToDiscordLimit(t *testing.T) {
	hs := newHarness(t)
	for i := range 200 {
		hs.store.links = append(hs.store.links, store.Link{GitHubID: int64(i + 1), GitHubLogin: fmt.Sprintf("user-number-%03d", i), DiscordID: fmt.Sprintf("1%017d", i)})
	}

	code, resp := hs.post(t, adminCmd("list"))

	expectReply(t, code, resp, "more")
	if n := len([]rune(resp.Data.Content)); n > maxReplyRunes {
		t.Fatalf("reply has %d runes", n)
	}
}

func TestUnlinked(t *testing.T) {
	hs := newHarness(t)
	hs.store.links = []store.Link{{GitHubID: 1, GitHubLogin: "Alice", DiscordID: userID}}
	hs.store.ignored = []store.Ignored{{GitHubID: 4, GitHubLogin: "malice"}}

	code, resp := hs.post(t, adminCmd("unlinked"))

	expectReply(t, code, resp, "without a link (2)", "`bob`", "`carol`")
	if strings.Contains(resp.Data.Content, "Alice") || strings.Contains(resp.Data.Content, "malice") {
		t.Fatalf("linked/ignored users listed: %q", resp.Data.Content)
	}

	hs.store.links = append(hs.store.links, store.Link{GitHubID: 2}, store.Link{GitHubID: 3})
	code, resp = hs.post(t, adminCmd("unlinked"))
	expectReply(t, code, resp, "Everyone in my-org is linked")
}

func TestIgnoreAndUnignore(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, adminCmd("ignore", "github", "dependabot"))
	expectReply(t, code, resp, "Ignoring", "`dependabot`")
	if len(hs.store.ignored) != 1 || hs.store.ignored[0].GitHubID != 9 || hs.store.ignored[0].CreatedBy != adminID {
		t.Fatalf("ignored = %+v", hs.store.ignored)
	}

	code, resp = hs.post(t, adminCmd("unignore", "github", "DEPENDABOT"))
	expectReply(t, code, resp, "No longer ignoring", "`dependabot`")

	code, resp = hs.post(t, adminCmd("unignore", "github", "dependabot"))
	expectReply(t, code, resp, "isn't ignored")

	if len(hs.ann.sent) != 2 {
		t.Fatalf("announcements = %d, want 2", len(hs.ann.sent))
	}
}

func TestIgnoreUnknownUser(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, adminCmd("ignore", "github", "ghost"))

	expectReply(t, code, resp, "not found")
}

func TestUnknownSubcommand(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, adminCmd("explode"))

	expectReply(t, code, resp, "Unknown command")
}

func TestUnknownTopLevelCommand(t *testing.T) {
	hs := newHarness(t)
	body := strings.Replace(adminCmd("list"), `"name":"admin"`, `"name":"other"`, 1)

	code, resp := hs.post(t, body)

	expectReply(t, code, resp, "Unknown command")
}

// --- autocomplete --------------------------------------------------------------

func TestAutocompleteOrgMembers(t *testing.T) {
	hs := newHarness(t)

	code, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "link", "user", userID, "github*", "ali"))

	if code != http.StatusOK || resp.Type != responseAutocomplete {
		t.Fatalf("code=%d type=%d", code, resp.Type)
	}
	var names []string
	for _, c := range resp.Data.Choices {
		names = append(names, c.Value)
	}
	// Prefix matches first, then substring matches.
	if strings.Join(names, ",") != "Alice,malice" {
		t.Fatalf("choices = %v", names)
	}
}

func TestAutocompleteEmptyQueryAndLimit(t *testing.T) {
	hs := newHarness(t)
	for i := range 40 {
		hs.dir.members = append(hs.dir.members, githubapi.User{ID: int64(100 + i), Login: fmt.Sprintf("dev%02d", i)})
	}

	_, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "ignore", "github*", ""))

	if len(resp.Data.Choices) != maxChoices {
		t.Fatalf("choices = %d, want %d", len(resp.Data.Choices), maxChoices)
	}
}

func TestAutocompleteUnignoreUsesIgnoredList(t *testing.T) {
	hs := newHarness(t)
	hs.store.ignored = []store.Ignored{{GitHubID: 9, GitHubLogin: "dependabot"}}

	_, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "unignore", "github*", "dep"))

	if len(resp.Data.Choices) != 1 || resp.Data.Choices[0].Value != "dependabot" {
		t.Fatalf("choices = %+v", resp.Data.Choices)
	}
}

func TestAutocompleteErrorReturnsNoChoices(t *testing.T) {
	hs := newHarness(t)
	hs.dir.err = errors.New("down")

	code, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "link", "github*", "a"))

	if code != http.StatusOK || len(resp.Data.Choices) != 0 {
		t.Fatalf("code=%d choices=%v", code, resp.Data.Choices)
	}
}

func TestResponsesAreValidJSONWithNoNullChoices(t *testing.T) {
	hs := newHarness(t)

	ts := strconv.FormatInt(hs.now.Unix(), 10)
	body := command(typeAutocomplete, userID, guildID, "link", "github*", "x")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(hs.priv, []byte(ts+body))))
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)

	if !bytes.Contains(rec.Body.Bytes(), []byte(`"choices":[]`)) {
		t.Fatalf("choices must be [] not null: %s", rec.Body.String())
	}
}

func TestLinkReportsReplacedLinks(t *testing.T) {
	hs := newHarness(t)
	hs.store.links = []store.Link{{GitHubID: 2, GitHubLogin: "bob", DiscordID: userID}}

	code, resp := hs.post(t, adminCmd("link", "user", userID, "github", "alice"))

	expectReply(t, code, resp, "Linked `Alice`", "replaced", "`bob`")
	if len(hs.ann.sent) != 1 || !strings.Contains(hs.ann.sent[0].Content, "`bob`") {
		t.Fatalf("announcement should mention the replaced link: %+v", hs.ann.sent)
	}
}

func TestSlowAnnouncementDoesNotDelayReply(t *testing.T) {
	hs := newHarness(t)
	hs.ann.delay = 300 * time.Millisecond
	ts := strconv.FormatInt(hs.now.Unix(), 10)
	body := adminCmd("link", "user", userID, "github", "alice")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(hs.priv, []byte(ts+body))))
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()

	start := time.Now()
	hs.h.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK || elapsed >= hs.ann.delay {
		t.Fatalf("reply took %v (announcement delay %v)", elapsed, hs.ann.delay)
	}
	hs.h.Wait()
	if len(hs.ann.sent) != 1 {
		t.Fatal("announcement should still be delivered after the reply")
	}
}

// --- watchers ------------------------------------------------------------------

func linkedHarness(t *testing.T) *harness {
	hs := newHarness(t)
	hs.store.links = []store.Link{{GitHubID: 1, GitHubLogin: "Alice", DiscordID: userID}}
	return hs
}

func TestWatchOrgWide(t *testing.T) {
	hs := linkedHarness(t)

	code, resp := hs.post(t, adminCmd("watch", "user", userID, "level", "new-prs", "repo", "*"))

	expectReply(t, code, resp, "<@"+userID+">", "new PRs", "the whole org")
	want := store.Watch{DiscordID: userID, Repo: "", Level: "new-prs", CreatedBy: adminID}
	if len(hs.store.watches) != 1 || hs.store.watches[0] != want {
		t.Fatalf("watches = %+v", hs.store.watches)
	}
	if len(hs.ann.sent) != 1 || !strings.Contains(hs.ann.sent[0].Content, "new PRs") {
		t.Fatalf("announcement = %+v", hs.ann.sent)
	}
}

func TestWatchRepo(t *testing.T) {
	tests := []struct {
		repo, wantRepo string
	}{
		{"api", "api"},
		{"my-org/api", "api"},
		{"MY-ORG/Web.App", "Web.App"},
	}
	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			hs := linkedHarness(t)

			code, resp := hs.post(t, adminCmd("watch", "user", userID, "level", "all", "repo", tt.repo))

			expectReply(t, code, resp, "all activity", "`"+tt.wantRepo+"`")
			if len(hs.store.watches) != 1 || hs.store.watches[0].Repo != tt.wantRepo {
				t.Fatalf("watches = %+v", hs.store.watches)
			}
		})
	}
}

func TestWatchRejections(t *testing.T) {
	tests := []struct {
		name  string
		opts  []string
		setup func(*harness)
		want  string
	}{
		{name: "not linked", opts: []string{"user", userID, "level", "prs", "repo", "*"}, setup: func(h *harness) { h.store.links = nil }, want: "isn't linked"},
		{name: "bot", opts: []string{"user", "999999999999999999", "level", "prs"}, want: "bot"},
		{name: "unknown level", opts: []string{"user", userID, "level", "everything"}, want: "level"},
		{name: "missing level", opts: []string{"user", userID}, want: "Missing"},
		{name: "other org", opts: []string{"user", userID, "level", "prs", "repo", "other-org/api"}, want: "my-org"},
		{name: "bad repo name", opts: []string{"user", userID, "level", "prs", "repo", "api; rm -rf"}, want: "isn't a valid repository"},
		{name: "missing repo", opts: []string{"user", userID, "level", "prs"}, want: "`*` for the whole org"},
		{name: "blank repo", opts: []string{"user", userID, "level", "prs", "repo", "  "}, want: "`*` for the whole org"},
		{name: "store down", opts: []string{"user", userID, "level", "prs", "repo", "*"}, setup: func(h *harness) { h.store.err = errors.New("disk") }, want: "Something went wrong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := linkedHarness(t)
			if tt.setup != nil {
				tt.setup(hs)
			}

			code, resp := hs.post(t, adminCmd("watch", tt.opts...))

			expectReply(t, code, resp, tt.want)
			if len(hs.store.watches) != 0 || len(hs.ann.sent) != 0 {
				t.Fatalf("rejected watch must not be stored or announced")
			}
		})
	}
}

func TestUnwatch(t *testing.T) {
	hs := linkedHarness(t)
	hs.store.watches = []store.Watch{{DiscordID: userID, Repo: "api", Level: "all"}}

	code, resp := hs.post(t, adminCmd("unwatch", "user", userID, "repo", "my-org/API"))
	expectReply(t, code, resp, "no longer watches", "`api`")
	if len(hs.store.watches) != 0 || len(hs.ann.sent) != 1 {
		t.Fatalf("watches=%+v announcements=%d", hs.store.watches, len(hs.ann.sent))
	}

	code, resp = hs.post(t, adminCmd("unwatch", "user", userID, "repo", "*"))
	expectReply(t, code, resp, "isn't watching", "the whole org")
}

func TestUnwatchOrgWideLeavesRepoWatches(t *testing.T) {
	hs := linkedHarness(t)
	hs.store.watches = []store.Watch{
		{DiscordID: userID, Repo: "", Level: "new-prs"},
		{DiscordID: userID, Repo: "shagan", Level: "new-prs"},
	}

	code, resp := hs.post(t, adminCmd("unwatch", "user", userID, "repo", "*"))

	expectReply(t, code, resp, "no longer watches", "the whole org")
	if len(hs.store.watches) != 1 || hs.store.watches[0].Repo != "shagan" {
		t.Fatalf("watches = %+v", hs.store.watches)
	}
}

func TestUnwatchRequiresRepo(t *testing.T) {
	hs := linkedHarness(t)
	hs.store.watches = []store.Watch{{DiscordID: userID, Repo: "", Level: "new-prs"}}

	code, resp := hs.post(t, adminCmd("unwatch", "user", userID))

	expectReply(t, code, resp, "Missing `repo`")
	if len(hs.store.watches) != 1 {
		t.Fatalf("watches = %+v", hs.store.watches)
	}
}

func TestListShowsWatchers(t *testing.T) {
	hs := linkedHarness(t)
	hs.store.watches = []store.Watch{
		{DiscordID: userID, Repo: "", Level: "new-prs"},
		{DiscordID: userID, Repo: "api", Level: "all"},
	}

	code, resp := hs.post(t, adminCmd("list"))

	expectReply(t, code, resp, "**Watchers**", "<@"+userID+"> new-prs (org) · all (api)")
}

func TestAutocompleteRepos(t *testing.T) {
	hs := linkedHarness(t)
	hs.store.repos = []string{"api", "mobile-app", "web-api"}

	_, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "watch", "user", userID, "level", "prs", "repo*", "api"))

	var got []string
	for _, c := range resp.Data.Choices {
		got = append(got, c.Value)
	}
	if strings.Join(got, ",") != "api,web-api" {
		t.Fatalf("choices = %v", got)
	}
}

func repoChoices(t *testing.T, hs *harness, query string) []string {
	t.Helper()
	_, resp := hs.post(t, command(typeAutocomplete, adminID, guildID, "watch", "user", userID, "level", "prs", "repo*", query))
	var got []string
	for _, c := range resp.Data.Choices {
		got = append(got, c.Value)
	}
	return got
}

func TestAutocompleteReposListsOrgReposThenSeenOnes(t *testing.T) {
	hs := linkedHarness(t)
	hs.dir.repos = []string{"private-api", "api"}
	hs.store.repos = []string{"API", "deleted-repo"}

	got := repoChoices(t, hs, "")

	// GitHub's order (most recently pushed first), then seen repos GitHub
	// didn't list, without case-insensitive duplicates.
	if strings.Join(got, ",") != "*,private-api,api,deleted-repo" {
		t.Fatalf("choices = %v", got)
	}
}

func TestAutocompleteReposOffersWholeOrg(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"*", "*"},
		{"org", "*"},
		{"Whole", "*"},
		{"sha", "shagan"},
		{"r", "repo-r"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			hs := linkedHarness(t)
			hs.dir.repos = []string{"shagan", "repo-r"}

			if got := repoChoices(t, hs, tt.query); strings.Join(got, ",") != tt.want {
				t.Fatalf("choices = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestAutocompleteReposCapsAtLimitWithWholeOrg(t *testing.T) {
	hs := linkedHarness(t)
	for i := 0; i < 30; i++ {
		hs.dir.repos = append(hs.dir.repos, fmt.Sprintf("repo-%02d", i))
	}

	got := repoChoices(t, hs, "")

	if len(got) != maxChoices || got[0] != "*" {
		t.Fatalf("got %d choices, first %q", len(got), got[0])
	}
}

func TestAutocompleteReposFallsBackToSeenReposWhenGitHubFails(t *testing.T) {
	hs := linkedHarness(t)
	hs.dir.repoErr = errors.New("403 Resource not accessible")
	hs.store.repos = []string{"api"}

	if got := repoChoices(t, hs, "a"); strings.Join(got, ",") != "api" {
		t.Fatalf("choices = %v", got)
	}
}

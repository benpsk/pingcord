package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSetAndListWatches(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)

	mustWatch(t, s, Watch{DiscordID: "100", Repo: "", Level: "new-prs", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "100", Repo: "api", Level: "all", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "200", Repo: "web", Level: "prs", CreatedBy: "9"})
	// Same user + repo again updates the level instead of adding a row.
	mustWatch(t, s, Watch{DiscordID: "100", Repo: "API", Level: "prs", CreatedBy: "8"})

	got, err := s.ListWatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Watch{
		{DiscordID: "100", Repo: "", Level: "new-prs", CreatedBy: "9", CreatedAt: t0},
		{DiscordID: "100", Repo: "api", Level: "prs", CreatedBy: "8", CreatedAt: t0},
		{DiscordID: "200", Repo: "web", Level: "prs", CreatedBy: "9", CreatedAt: t0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watches =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSetWatchValidates(t *testing.T) {
	s, _ := newTestStore(t)
	for _, w := range []Watch{
		{Repo: "", Level: "prs", CreatedBy: "9"},
		{DiscordID: "1", Level: "everything", CreatedBy: "9"},
		{DiscordID: "1", Level: "prs"},
	} {
		if err := s.SetWatch(context.Background(), w); err == nil {
			t.Errorf("SetWatch(%+v) expected error", w)
		}
	}
}

func TestRemoveWatch(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustWatch(t, s, Watch{DiscordID: "100", Repo: "api", Level: "all", CreatedBy: "9"})

	got, err := s.RemoveWatch(ctx, "100", "API")
	if err != nil || got.Level != "all" {
		t.Fatalf("RemoveWatch = %+v, %v", got, err)
	}
	if _, err := s.RemoveWatch(ctx, "100", "api"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove err = %v", err)
	}
}

func TestWatchersForJoinsLinksAndMatchesRepo(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	mustUpsert(t, s, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "200", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "100", Repo: "", Level: "new-prs", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "100", Repo: "api", Level: "all", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "200", Repo: "web", Level: "prs", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "300", Repo: "", Level: "all", CreatedBy: "9"}) // not linked

	got, err := s.WatchersFor(ctx, "API")
	if err != nil {
		t.Fatal(err)
	}
	want := []Watcher{
		{DiscordID: "100", Repo: "", Level: "new-prs", GitHubID: 1, GitHubLogin: "alice"},
		{DiscordID: "100", Repo: "api", Level: "all", GitHubID: 1, GitHubLogin: "alice"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WatchersFor(api) =\n%+v\nwant\n%+v", got, want)
	}
}

func TestUnlinkRemovesWatches(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "100", Level: "prs", CreatedBy: "9"})

	if _, err := s.DeleteLinkByDiscordID(ctx, "100"); err != nil {
		t.Fatal(err)
	}

	if got, _ := s.ListWatches(ctx); len(got) != 0 {
		t.Fatalf("watches survived unlink: %+v", got)
	}
}

func TestRelinkKeepsWatchesButMovingAccountAwayDropsThem(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	mustWatch(t, s, Watch{DiscordID: "100", Level: "prs", CreatedBy: "9"})

	// Discord 100 relinked to another GitHub account: the watch stays.
	mustUpsert(t, s, Link{GitHubID: 5, GitHubLogin: "alice2", DiscordID: "100", CreatedBy: "9"})
	if got, _ := s.ListWatches(ctx); len(got) != 1 {
		t.Fatalf("watch should follow the Discord user: %+v", got)
	}

	// GitHub 5 moved to Discord 200: Discord 100 has no link left, so its
	// watches go too.
	mustUpsert(t, s, Link{GitHubID: 5, GitHubLogin: "alice2", DiscordID: "200", CreatedBy: "9"})
	if got, _ := s.ListWatches(ctx); len(got) != 0 {
		t.Fatalf("orphaned watch kept: %+v", got)
	}
}

func TestSeenRepos(t *testing.T) {
	ctx := context.Background()
	s, now := newTestStore(t)

	for _, r := range []string{"web", "api", "API", "mobile"} {
		if err := s.RecordRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Minute)
	}
	if err := s.RecordRepo(ctx, ""); err != nil {
		t.Fatal("empty repo should be ignored, not an error")
	}

	got, err := s.ListRepos(ctx)
	if err != nil || !reflect.DeepEqual(got, []string{"api", "mobile", "web"}) {
		t.Fatalf("ListRepos = %v, %v", got, err)
	}
}

func TestWatchMethodsOnClosedStore(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	_ = s.Close()

	errs := []error{
		s.SetWatch(ctx, Watch{DiscordID: "1", Level: "prs", CreatedBy: "9"}),
		func() error { _, err := s.RemoveWatch(ctx, "1", ""); return err }(),
		func() error { _, err := s.ListWatches(ctx); return err }(),
		func() error { _, err := s.WatchersFor(ctx, "api"); return err }(),
		s.RecordRepo(ctx, "api"),
		func() error { _, err := s.ListRepos(ctx); return err }(),
	}
	for i, err := range errs {
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("call %d: err = %v", i, err)
		}
	}
}

func mustWatch(t *testing.T, s *Store, w Watch) {
	t.Helper()
	if err := s.SetWatch(context.Background(), w); err != nil {
		t.Fatalf("SetWatch(%+v): %v", w, err)
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := t0
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, &now
}

func TestOpenIsIdempotentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.UpsertLink(ctx, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"}); err != nil {
		t.Fatalf("UpsertLink: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()
	links, err := s2.ListLinks(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("data not persisted: %v %v", links, err)
	}
}

func TestOpenFailsOnBadPath(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing-dir", "x.db"))
	if err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

func TestUpsertAndListLinks(t *testing.T) {
	ctx := context.Background()
	s, now := newTestStore(t)

	mustUpsert(t, s, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "200", CreatedBy: "9"})
	*now = now.Add(time.Hour)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "Alice", DiscordID: "100", CreatedBy: "9"})

	links, err := s.ListLinks(ctx)
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links) != 2 || links[0].GitHubLogin != "Alice" || links[1].GitHubLogin != "bob" {
		t.Fatalf("links not sorted case-insensitively by login: %+v", links)
	}
	if !links[0].CreatedAt.Equal(t0.Add(time.Hour)) || !links[1].CreatedAt.Equal(t0) {
		t.Fatalf("timestamps wrong: %+v", links)
	}
}

func TestUpsertLinkReplacesBothSides(t *testing.T) {
	ctx := context.Background()
	s, now := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	mustUpsert(t, s, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "200", CreatedBy: "9"})
	*now = now.Add(time.Minute)

	// Discord user 100 is now linked to GitHub 2: both old links must go.
	replaced, err := s.UpsertLink(ctx, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "100", CreatedBy: "8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 2 || replaced[0].GitHubID != 1 || replaced[1].DiscordID != "200" {
		t.Fatalf("replaced = %+v, want alice→100 and bob→200", replaced)
	}
	if again, _ := s.UpsertLink(ctx, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "100", CreatedBy: "8"}); len(again) != 0 {
		t.Fatalf("re-linking the same pair should replace nothing, got %+v", again)
	}

	links, _ := s.ListLinks(ctx)
	if len(links) != 1 || links[0].GitHubID != 2 || links[0].DiscordID != "100" || links[0].CreatedBy != "8" {
		t.Fatalf("links = %+v", links)
	}
	if !links[0].UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("UpdatedAt = %v", links[0].UpdatedAt)
	}
}

func TestUpsertLinkValidates(t *testing.T) {
	s, _ := newTestStore(t)
	for _, l := range []Link{
		{GitHubLogin: "a", DiscordID: "1", CreatedBy: "9"},
		{GitHubID: 1, DiscordID: "1", CreatedBy: "9"},
		{GitHubID: 1, GitHubLogin: "a", CreatedBy: "9"},
	} {
		if _, err := s.UpsertLink(context.Background(), l); err == nil {
			t.Errorf("UpsertLink(%+v) expected error", l)
		}
	}
}

func TestDeleteLinkByDiscordID(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})

	got, err := s.DeleteLinkByDiscordID(ctx, "100")
	if err != nil || got.GitHubLogin != "alice" {
		t.Fatalf("DeleteLinkByDiscordID = %+v, %v", got, err)
	}
	if _, err := s.DeleteLinkByDiscordID(ctx, "100"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestLinksByGitHubIDs(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	mustUpsert(t, s, Link{GitHubID: 2, GitHubLogin: "bob", DiscordID: "200", CreatedBy: "9"})

	got, err := s.LinksByGitHubIDs(ctx, []int64{2, 3, 2})
	if err != nil {
		t.Fatalf("LinksByGitHubIDs: %v", err)
	}
	if len(got) != 1 || got[2].DiscordID != "200" {
		t.Fatalf("got %+v", got)
	}

	empty, err := s.LinksByGitHubIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty input: %v %v", empty, err)
	}
}

func TestIgnore(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)

	if err := s.Ignore(ctx, 7, "dependabot", "9"); err != nil {
		t.Fatalf("Ignore: %v", err)
	}
	if err := s.Ignore(ctx, 7, "dependabot", "9"); err != nil {
		t.Fatalf("Ignore twice should be a no-op: %v", err)
	}
	if err := s.Ignore(ctx, 0, "x", "9"); err == nil {
		t.Fatal("Ignore with zero id should fail")
	}

	ids, err := s.IgnoredIDs(ctx, []int64{7, 8})
	if err != nil || len(ids) != 1 || !ids[7] {
		t.Fatalf("IgnoredIDs = %v, %v", ids, err)
	}
	list, err := s.ListIgnored(ctx)
	if err != nil || len(list) != 1 || list[0].GitHubLogin != "dependabot" {
		t.Fatalf("ListIgnored = %+v, %v", list, err)
	}

	got, err := s.Unignore(ctx, "DependaBot")
	if err != nil || got.GitHubID != 7 {
		t.Fatalf("Unignore = %+v, %v", got, err)
	}
	if _, err := s.Unignore(ctx, "dependabot"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Unignore err = %v", err)
	}
}

func TestRefreshLogins(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustUpsert(t, s, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "100", CreatedBy: "9"})
	_ = s.Ignore(ctx, 2, "oldbot", "9")
	_ = s.CacheLogin(ctx, "alice", 1)

	err := s.RefreshLogins(ctx, map[int64]string{1: "alice-renamed", 2: "newbot", 3: "stranger"})
	if err != nil {
		t.Fatalf("RefreshLogins: %v", err)
	}

	links, _ := s.ListLinks(ctx)
	if links[0].GitHubLogin != "alice-renamed" {
		t.Errorf("link login = %q", links[0].GitHubLogin)
	}
	ign, _ := s.ListIgnored(ctx)
	if ign[0].GitHubLogin != "newbot" {
		t.Errorf("ignored login = %q", ign[0].GitHubLogin)
	}
	// The old login must no longer resolve to the renamed user...
	if _, _, found, _ := s.LookupLogin(ctx, "alice"); found {
		t.Error("stale login still cached")
	}
	// ...and the new logins must resolve.
	if id, _, found, _ := s.LookupLogin(ctx, "ALICE-RENAMED"); !found || id != 1 {
		t.Errorf("new login not cached: %d %v", id, found)
	}
	if id, _, found, _ := s.LookupLogin(ctx, "stranger"); !found || id != 3 {
		t.Errorf("unlinked user not cached: %d %v", id, found)
	}
}

func TestLoginCache(t *testing.T) {
	ctx := context.Background()
	s, now := newTestStore(t)

	if _, _, found, err := s.LookupLogin(ctx, "alice"); found || err != nil {
		t.Fatalf("empty cache lookup: %v %v", found, err)
	}
	if err := s.CacheLogin(ctx, "Alice", 1); err != nil {
		t.Fatalf("CacheLogin: %v", err)
	}
	*now = now.Add(time.Hour)
	if err := s.CacheLogin(ctx, "alice", 1); err != nil {
		t.Fatalf("CacheLogin update: %v", err)
	}
	id, fetched, found, err := s.LookupLogin(ctx, "ALICE")
	if err != nil || !found || id != 1 || !fetched.Equal(t0.Add(time.Hour)) {
		t.Fatalf("LookupLogin = %d %v %v %v", id, fetched, found, err)
	}
}

func TestClosedStoreReturnsErrors(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	_ = s.Close()

	checks := map[string]error{
		"ListLinks": func() error { _, err := s.ListLinks(ctx); return err }(),
		"UpsertLink": func() error {
			_, err := s.UpsertLink(ctx, Link{GitHubID: 1, GitHubLogin: "a", DiscordID: "1", CreatedBy: "9"})
			return err
		}(),
		"LinksByGitHubIDs": func() error { _, err := s.LinksByGitHubIDs(ctx, []int64{1}); return err }(),
		"IgnoredIDs":       func() error { _, err := s.IgnoredIDs(ctx, []int64{1}); return err }(),
		"ListIgnored":      func() error { _, err := s.ListIgnored(ctx); return err }(),
		"Ignore":           s.Ignore(ctx, 1, "a", "9"),
		"Unignore":         func() error { _, err := s.Unignore(ctx, "a"); return err }(),
		"Delete":           func() error { _, err := s.DeleteLinkByDiscordID(ctx, "1"); return err }(),
		"RefreshLogins":    s.RefreshLogins(ctx, map[int64]string{1: "a"}),
		"CacheLogin":       s.CacheLogin(ctx, "a", 1),
		"LookupLogin":      func() error { _, _, _, err := s.LookupLogin(ctx, "a"); return err }(),
	}
	for name, err := range checks {
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s on closed store: err = %v", name, err)
		}
	}
}

func mustUpsert(t *testing.T, s *Store, l Link) {
	t.Helper()
	if _, err := s.UpsertLink(context.Background(), l); err != nil {
		t.Fatalf("UpsertLink(%+v): %v", l, err)
	}
}

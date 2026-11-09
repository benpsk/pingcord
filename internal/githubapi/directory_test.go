package githubapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// memCache is an in-memory LoginCache.
type memCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	failGet bool
}

type cacheEntry struct {
	id int64
	at time.Time
}

func newMemCache() *memCache { return &memCache{entries: map[string]cacheEntry{}} }

func (m *memCache) LookupLogin(_ context.Context, login string) (int64, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return 0, time.Time{}, false, errors.New("db down")
	}
	e, ok := m.entries[strings.ToLower(login)]
	return e.id, e.at, ok, nil
}

func (m *memCache) CacheLogin(_ context.Context, login string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[strings.ToLower(login)] = cacheEntry{id: id, at: time.Now()}
	return nil
}

func newTestDirectory(t *testing.T) (*Directory, *fakeGitHub, *memCache, *time.Time) {
	t.Helper()
	f := newFakeGitHub(t)
	cache := newMemCache()
	now := time.Now()
	d := NewDirectory(f.client("my-org"), cache, WithDirectoryClock(func() time.Time { return now }))
	return d, f, cache, &now
}

func TestResolveLoginsUsesCacheThenAPI(t *testing.T) {
	ctx := context.Background()
	d, f, cache, _ := newTestDirectory(t)
	cache.entries["carol"] = cacheEntry{id: 3, at: time.Now()}

	got, err := d.ResolveLogins(ctx, []string{"alice", "carol", "ghost"})

	if err != nil {
		t.Fatalf("ResolveLogins: %v", err)
	}
	if len(got) != 2 || got["alice"] != 1 || got["carol"] != 3 {
		t.Fatalf("got %v", got)
	}
	if f.calls.Load() != 2 { // alice + ghost; carol from cache
		t.Fatalf("API calls = %d, want 2", f.calls.Load())
	}
	if cache.entries["alice"].id != 1 {
		t.Fatal("API result not written to cache")
	}

	// Second call: alice cached, ghost negatively cached → no API calls.
	if _, err := d.ResolveLogins(ctx, []string{"alice", "ghost"}); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("API calls after repeat = %d, want 2", f.calls.Load())
	}
}

func TestResolveLoginsRefreshesStaleEntries(t *testing.T) {
	d, f, cache, _ := newTestDirectory(t)
	cache.entries["alice"] = cacheEntry{id: 1, at: time.Now().Add(-8 * 24 * time.Hour)}

	if _, err := d.ResolveLogins(context.Background(), []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("stale entry not refreshed; calls = %d", f.calls.Load())
	}
}

func TestResolveLoginsFallsBackToStaleCacheOnAPIError(t *testing.T) {
	d, f, cache, _ := newTestDirectory(t)
	cache.entries["alice"] = cacheEntry{id: 1, at: time.Now().Add(-8 * 24 * time.Hour)}
	f.fail.Store(true)

	got, err := d.ResolveLogins(context.Background(), []string{"alice", "bob"})

	if got["alice"] != 1 {
		t.Fatalf("stale cache not used: %v", got)
	}
	if _, ok := got["bob"]; ok || err == nil {
		t.Fatalf("bob should fail with an error; got %v, err %v", got, err)
	}
}

func TestResolveLoginsCacheReadErrorStillUsesAPI(t *testing.T) {
	d, _, cache, _ := newTestDirectory(t)
	cache.failGet = true

	got, err := d.ResolveLogins(context.Background(), []string{"alice"})

	if got["alice"] != 1 || err != nil {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestNegativeCacheExpires(t *testing.T) {
	d, f, _, now := newTestDirectory(t)
	ctx := context.Background()
	_, _ = d.ResolveLogins(ctx, []string{"ghost"})
	*now = now.Add(2 * time.Hour)

	_, _ = d.ResolveLogins(ctx, []string{"ghost"})

	if f.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after negative TTL", f.calls.Load())
	}
}

func TestLookupUserAlwaysAsksAPIAndCaches(t *testing.T) {
	d, f, cache, _ := newTestDirectory(t)

	u, err := d.LookupUser(context.Background(), "ALICE")

	if err != nil || u.Login != "Alice" || f.calls.Load() != 1 || cache.entries["alice"].id != 1 {
		t.Fatalf("LookupUser = %+v, %v (calls %d)", u, err, f.calls.Load())
	}
}

func TestListsAreCachedAndServedStaleOnError(t *testing.T) {
	d, f, _, now := newTestDirectory(t)
	ctx := context.Background()

	members, err := d.OrgMembers(ctx)
	if err != nil || len(members) != 3 {
		t.Fatalf("OrgMembers = %v, %v", members, err)
	}
	team, err := d.TeamMembers(ctx, "My-Org/Backend")
	if err != nil || len(team) != 2 {
		t.Fatalf("TeamMembers = %v, %v", team, err)
	}
	calls := f.calls.Load()

	_, _ = d.OrgMembers(ctx)
	_, _ = d.TeamMembers(ctx, "my-org/backend")
	if f.calls.Load() != calls {
		t.Fatal("lists not served from cache")
	}

	*now = now.Add(11 * time.Minute)
	f.fail.Store(true)
	members, err = d.OrgMembers(ctx)
	if err != nil || len(members) != 3 {
		t.Fatalf("stale list not served on error: %v, %v", members, err)
	}
}

func TestListErrorWithoutCacheIsReturned(t *testing.T) {
	d, f, _, _ := newTestDirectory(t)
	f.fail.Store(true)

	if _, err := d.TeamMembers(context.Background(), "my-org/backend"); err == nil {
		t.Fatal("expected error")
	}
}

package githubapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	loginTTL    = 7 * 24 * time.Hour
	listTTL     = 10 * time.Minute
	negativeTTL = time.Hour
)

// LoginCache persists login → ID lookups (implemented by store.Store).
type LoginCache interface {
	LookupLogin(ctx context.Context, login string) (id int64, fetchedAt time.Time, found bool, err error)
	CacheLogin(ctx context.Context, login string, id int64) error
}

// Directory answers identity questions with caching in front of the API:
// logins are cached persistently, member lists in memory, and unknown logins
// are negatively cached so "@param" in a doc comment doesn't hit the API on
// every event. When the API fails, stale cached data is served.
type Directory struct {
	api   *Client
	cache LoginCache
	now   func() time.Time
	log   *slog.Logger

	mu     sync.Mutex
	lists  map[string]cachedList
	misses map[string]time.Time
}

type cachedList struct {
	users []User
	at    time.Time
}

// DirectoryOption configures a Directory.
type DirectoryOption func(*Directory)

// WithDirectoryClock overrides the time source (for tests).
func WithDirectoryClock(now func() time.Time) DirectoryOption {
	return func(d *Directory) { d.now = now }
}

// WithLogger sets the logger used for non-fatal cache problems.
func WithLogger(l *slog.Logger) DirectoryOption {
	return func(d *Directory) { d.log = l }
}

// NewDirectory wraps api with caching.
func NewDirectory(api *Client, cache LoginCache, opts ...DirectoryOption) *Directory {
	d := &Directory{
		api:    api,
		cache:  cache,
		now:    time.Now,
		log:    slog.Default(),
		lists:  map[string]cachedList{},
		misses: map[string]time.Time{},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// ResolveLogins maps logins (as written in @mentions) to GitHub IDs. Logins
// that don't exist are omitted. Lookup failures are returned joined, together
// with every login that could be resolved.
func (d *Directory) ResolveLogins(ctx context.Context, logins []string) (map[string]int64, error) {
	out := make(map[string]int64, len(logins))
	var errs []error
	for _, login := range logins {
		key := strings.ToLower(login)
		id, ok, err := d.resolve(ctx, key)
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			out[key] = id
		}
	}
	return out, errors.Join(errs...)
}

func (d *Directory) resolve(ctx context.Context, login string) (int64, bool, error) {
	if d.recentlyMissing(login) {
		return 0, false, nil
	}

	cachedID, fetchedAt, found, err := d.cache.LookupLogin(ctx, login)
	if err != nil {
		d.log.WarnContext(ctx, "login cache read failed", "login", login, "error", err)
		found = false
	}
	if found && d.now().Sub(fetchedAt) < loginTTL {
		return cachedID, true, nil
	}

	u, err := d.api.User(ctx, login)
	switch {
	case errors.Is(err, ErrNotFound):
		d.markMissing(login)
		return 0, false, nil
	case err != nil && found:
		d.log.WarnContext(ctx, "github lookup failed, using stale cache", "login", login, "error", err)
		return cachedID, true, nil
	case err != nil:
		return 0, false, fmt.Errorf("resolve @%s: %w", login, err)
	}
	d.store(ctx, login, u.ID)
	return u.ID, true, nil
}

// LookupUser fetches a user from the API (for admin commands that need the
// canonical login) and refreshes the cache.
func (d *Directory) LookupUser(ctx context.Context, login string) (User, error) {
	u, err := d.api.User(ctx, login)
	if err != nil {
		return User{}, err
	}
	d.store(ctx, u.Login, u.ID)
	return u, nil
}

func (d *Directory) store(ctx context.Context, login string, id int64) {
	if err := d.cache.CacheLogin(ctx, login, id); err != nil {
		d.log.WarnContext(ctx, "login cache write failed", "login", login, "error", err)
	}
}

// OrgMembers returns the organization's members.
func (d *Directory) OrgMembers(ctx context.Context) ([]User, error) {
	return d.list(ctx, "org", d.api.OrgMembers)
}

// TeamMembers returns members of team "org/slug".
func (d *Directory) TeamMembers(ctx context.Context, team string) ([]User, error) {
	team = strings.ToLower(team)
	return d.list(ctx, "team:"+team, func(ctx context.Context) ([]User, error) {
		return d.api.TeamMembers(ctx, team)
	})
}

func (d *Directory) list(ctx context.Context, key string, fetch func(context.Context) ([]User, error)) ([]User, error) {
	d.mu.Lock()
	cached, ok := d.lists[key]
	d.mu.Unlock()
	if ok && d.now().Sub(cached.at) < listTTL {
		return cached.users, nil
	}

	users, err := fetch(ctx)
	if err != nil {
		if ok {
			d.log.WarnContext(ctx, "github list failed, using stale cache", "list", key, "error", err)
			return cached.users, nil
		}
		return nil, err
	}

	d.mu.Lock()
	d.lists[key] = cachedList{users: users, at: d.now()}
	d.mu.Unlock()
	return users, nil
}

func (d *Directory) recentlyMissing(login string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.misses[login]
	return ok && d.now().Sub(at) < negativeTTL
}

func (d *Directory) markMissing(login string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.misses[login] = d.now()
}

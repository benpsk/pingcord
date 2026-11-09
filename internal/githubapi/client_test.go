package githubapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub serves a tiny subset of the GitHub REST API.
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	calls    atomic.Int64
	fail     atomic.Bool
	members  []User // org members, served 2 per page
	team     []User
	expiry   atomic.Value // string
	lastAuth atomic.Value
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{
		t:       t,
		members: []User{{1, "alice"}, {2, "bob"}, {3, "carol"}},
		team:    []User{{2, "bob"}, {3, "carol"}},
	}
	f.expiry.Store("2026-11-20 10:00:00 UTC")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{login}", func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(r.PathValue("login")) {
		case "alice":
			fmt.Fprint(w, `{"id":1,"login":"Alice","type":"User"}`)
		case "bob":
			fmt.Fprint(w, `{"id":2,"login":"bob","type":"User"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	})
	mux.HandleFunc("GET /orgs/my-org/members", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		start := 0
		if page == "2" {
			start = 2
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s/orgs/my-org/members?per_page=100&page=2>; rel="next", <%s/orgs/my-org/members?page=2>; rel="last"`, f.srv.URL, f.srv.URL))
		}
		end := min(start+2, len(f.members))
		writeUsers(w, f.members[start:end])
	})
	mux.HandleFunc("GET /orgs/my-org/teams/backend/members", func(w http.ResponseWriter, _ *http.Request) {
		writeUsers(w, f.team)
	})
	mux.HandleFunc("GET /orgs/evil/members", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://attacker.example/steal>; rel="next"`)
		writeUsers(w, nil)
	})

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.lastAuth.Store(r.Header.Get("Authorization"))
		if f.fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message":"upstream down"}`)
			return
		}
		if exp := f.expiry.Load().(string); exp != "" {
			w.Header().Set("GitHub-Authentication-Token-Expiration", exp)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeUsers(w http.ResponseWriter, users []User) {
	parts := make([]string, len(users))
	for i, u := range users {
		parts[i] = fmt.Sprintf(`{"id":%d,"login":%q}`, u.ID, u.Login)
	}
	fmt.Fprint(w, "["+strings.Join(parts, ",")+"]")
}

func (f *fakeGitHub) client(org string) *Client {
	return NewClient(f.srv.URL, "test-token", org, f.srv.Client())
}

func TestClientUser(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client("my-org")

	u, err := c.User(context.Background(), "alice")
	if err != nil || u != (User{ID: 1, Login: "Alice"}) {
		t.Fatalf("User = %+v, %v", u, err)
	}
	if got := f.lastAuth.Load(); got != "Bearer test-token" {
		t.Errorf("Authorization = %v", got)
	}

	if _, err := c.User(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user err = %v, want ErrNotFound", err)
	}
	if _, err := c.User(context.Background(), "../orgs/x"); err == nil {
		t.Fatal("invalid login should be rejected before any request")
	}
}

func TestClientOrgMembersPaginates(t *testing.T) {
	f := newFakeGitHub(t)

	got, err := f.client("my-org").OrgMembers(context.Background())

	if err != nil {
		t.Fatalf("OrgMembers: %v", err)
	}
	if len(got) != 3 || got[2].Login != "carol" {
		t.Fatalf("members = %+v", got)
	}
}

func TestClientRefusesCrossHostPagination(t *testing.T) {
	f := newFakeGitHub(t)

	_, err := f.client("evil").OrgMembers(context.Background())

	if err == nil || !strings.Contains(err.Error(), "unexpected host") {
		t.Fatalf("err = %v, want cross-host refusal", err)
	}
}

func TestClientTeamMembers(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client("my-org")

	got, err := c.TeamMembers(context.Background(), "my-org/backend")
	if err != nil || len(got) != 2 {
		t.Fatalf("TeamMembers = %+v, %v", got, err)
	}
	if _, err := c.TeamMembers(context.Background(), "my-org/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown team err = %v", err)
	}
	if _, err := c.TeamMembers(context.Background(), "no-slash"); err == nil {
		t.Fatal("malformed team should fail")
	}
}

func TestClientServerErrorIncludesStatusAndMessage(t *testing.T) {
	f := newFakeGitHub(t)
	f.fail.Store(true)

	_, err := f.client("my-org").User(context.Background(), "alice")

	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream down") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatal("error leaks token")
	}
}

func TestClientCheckAccessReportsTokenExpiry(t *testing.T) {
	f := newFakeGitHub(t)

	expiry, err := f.client("my-org").CheckAccess(context.Background())

	if err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	want := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if !expiry.Equal(want) {
		t.Fatalf("expiry = %v, want %v", expiry, want)
	}

	f.expiry.Store("")
	expiry, err = f.client("my-org").CheckAccess(context.Background())
	if err != nil || !expiry.IsZero() {
		t.Fatalf("no-expiry token: %v, %v", expiry, err)
	}
}

func TestClientHonoursContextCancellation(t *testing.T) {
	f := newFakeGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.client("my-org").User(ctx, "alice"); err == nil {
		t.Fatal("expected context error")
	}
}

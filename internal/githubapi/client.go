// Package githubapi is a minimal, read-only GitHub REST client for the
// lookups pingcord needs: username → ID, org members, team members and the
// org's repositories.
package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	apiVersion     = "2022-11-28"
	userAgent      = "pingcord"
	perPage        = "100"
	maxPages       = 20 // 2,000 members or repos is plenty for a team tool
	maxBodyBytes   = 5 << 20
	maxErrorMsgLen = 200
	expiryHeader   = "GitHub-Authentication-Token-Expiration"
	defaultTimeout = 10 * time.Second
)

var (
	// ErrNotFound means the user, org or team does not exist or is not visible.
	ErrNotFound = errors.New("github: not found")

	loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	slugRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	linkRe  = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

// User is a GitHub account identity.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Client calls the GitHub REST API with a token.
type Client struct {
	base  *url.URL
	token string
	org   string
	http  *http.Client
}

// NewClient returns a client for baseURL (e.g. https://api.github.com).
// A nil httpClient gets a client with a sensible timeout.
func NewClient(baseURL, token, org string, httpClient *http.Client) *Client {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		base = &url.URL{Scheme: "https", Host: "api.github.com"}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{base: base, token: token, org: org, http: httpClient}
}

// Org returns the configured organization login.
func (c *Client) Org() string { return c.org }

// User looks up a user by login. Returns ErrNotFound if it does not exist.
func (c *Client) User(ctx context.Context, login string) (User, error) {
	if !loginRe.MatchString(login) {
		return User{}, fmt.Errorf("github: invalid login %q", login)
	}
	var u User
	if _, err := c.getJSON(ctx, c.endpoint("users", login), &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// OrgMembers lists all members of the configured organization.
func (c *Client) OrgMembers(ctx context.Context) ([]User, error) {
	return c.listUsers(ctx, c.endpoint("orgs", c.org, "members"))
}

// OrgRepos lists the names of the organization's repositories that aren't
// archived, most recently pushed first. Private repositories are included
// only when the token has repository access (Metadata: Read-only).
func (c *Client) OrgRepos(ctx context.Context) ([]string, error) {
	u := c.endpoint("orgs", c.org, "repos")
	q := u.Query()
	q.Set("type", "all")
	q.Set("sort", "pushed")
	u.RawQuery = q.Encode()

	repos, err := listPages[repo](ctx, c, u)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		if !r.Archived {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

type repo struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// TeamMembers lists members of team "org/slug".
func (c *Client) TeamMembers(ctx context.Context, team string) ([]User, error) {
	org, slug, ok := strings.Cut(team, "/")
	if !ok || !loginRe.MatchString(org) || !slugRe.MatchString(slug) {
		return nil, fmt.Errorf("github: invalid team %q", team)
	}
	return c.listUsers(ctx, c.endpoint("orgs", org, "teams", slug, "members"))
}

// CheckAccess verifies the token can read org members and returns the
// token's expiry time (zero if it does not expire or is not reported).
func (c *Client) CheckAccess(ctx context.Context) (time.Time, error) {
	u := c.endpoint("orgs", c.org, "members")
	q := u.Query()
	q.Set("per_page", "1")
	u.RawQuery = q.Encode()

	var discard []User
	header, err := c.getJSON(ctx, u, &discard)
	if err != nil {
		return time.Time{}, err
	}
	return parseExpiry(header.Get(expiryHeader)), nil
}

func parseExpiry(v string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (c *Client) endpoint(segments ...string) *url.URL {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.Join(escaped, "/")
	u.RawPath = ""
	return &u
}

func (c *Client) listUsers(ctx context.Context, u *url.URL) ([]User, error) {
	return listPages[User](ctx, c, u)
}

// listPages GETs u and every following page (per the Link header).
func listPages[T any](ctx context.Context, c *Client, u *url.URL) ([]T, error) {
	q := u.Query()
	q.Set("per_page", perPage)
	u.RawQuery = q.Encode()

	var all []T
	for page := 0; u != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("github: more than %d pages from %s", maxPages, u.Path)
		}
		var batch []T
		header, err := c.getJSON(ctx, u, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if u, err = c.nextPage(header.Get("Link")); err != nil {
			return nil, err
		}
	}
	return all, nil
}

// nextPage parses the Link header. It refuses to follow links to any host
// other than the API base, so a tampered header cannot leak the token.
func (c *Client) nextPage(link string) (*url.URL, error) {
	m := linkRe.FindStringSubmatch(link)
	if m == nil {
		return nil, nil
	}
	next, err := url.Parse(m[1])
	if err != nil {
		return nil, fmt.Errorf("github: bad pagination link: %w", err)
	}
	if next.Scheme != c.base.Scheme || next.Host != c.base.Host {
		return nil, fmt.Errorf("github: pagination link to unexpected host %q", next.Host)
	}
	return next, nil
}

func (c *Client) getJSON(ctx context.Context, u *url.URL, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: GET %s: %w", u.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("github: read %s: %w", u.Path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GET %s: %w", u.Path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("github: GET %s: status %d: %s", u.Path, resp.StatusCode, errorMessage(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("github: decode %s: %w", u.Path, err)
	}
	return resp.Header, nil
}

func errorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	if len(msg) > maxErrorMsgLen {
		msg = msg[:maxErrorMsgLen] + "…"
	}
	return msg
}

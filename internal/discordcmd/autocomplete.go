package discordcmd

import (
	"context"
	"strings"
)

// maxChoices is Discord's limit for autocomplete results.
const maxChoices = 25

// autocomplete suggests GitHub logins for the focused "github" option (org
// members for link/ignore, ignored users for unignore) and repository names
// for the "repo" option.
func (h *Handler) autocomplete(ctx context.Context, in *interaction) []choice {
	if !h.isAuthorized(in) {
		return nil
	}
	sub, opts := in.subcommand()
	var query, field string
	for _, o := range opts {
		if o.Focused {
			query, field = strings.ToLower(o.stringValue()), o.Name
		}
	}

	if field == "repo" {
		return rankChoices(h.candidateRepos(ctx), query)
	}
	logins, err := h.candidateLogins(ctx, sub)
	if err != nil {
		h.log.WarnContext(ctx, "autocomplete lookup failed", "subcommand", sub, "error", err)
		return nil
	}
	return rankChoices(logins, query)
}

// candidateRepos returns the org's repos from GitHub (most recently pushed
// first), then repos seen in webhooks that GitHub didn't list. Either source
// may fail; the other still gives suggestions.
func (h *Handler) candidateRepos(ctx context.Context) []string {
	fromGitHub, err := h.dir.OrgRepos(ctx)
	if err != nil {
		h.log.WarnContext(ctx, "autocomplete: listing org repos failed", "error", err)
	}
	seen, err := h.store.ListRepos(ctx)
	if err != nil {
		h.log.WarnContext(ctx, "autocomplete: listing seen repos failed", "error", err)
	}

	out := make([]string, 0, len(fromGitHub)+len(seen))
	have := make(map[string]bool, cap(out))
	// Two loops rather than append(fromGitHub, seen...): fromGitHub is the
	// directory's cached slice and must not be written to.
	for _, list := range [][]string{fromGitHub, seen} {
		for _, name := range list {
			if key := strings.ToLower(name); !have[key] {
				have[key] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func (h *Handler) candidateLogins(ctx context.Context, sub string) ([]string, error) {
	if sub == "unignore" {
		ignored, err := h.store.ListIgnored(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(ignored))
		for i, ig := range ignored {
			out[i] = ig.GitHubLogin
		}
		return out, nil
	}
	members, err := h.dir.OrgMembers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = m.Login
	}
	return out, nil
}

// rankChoices returns prefix matches first, then substring matches, keeping
// the original order within each group, capped at maxChoices.
func rankChoices(logins []string, query string) []choice {
	var prefix, contains []choice
	for _, l := range logins {
		lower := strings.ToLower(l)
		switch {
		case strings.HasPrefix(lower, query):
			prefix = append(prefix, choice{Name: l, Value: l})
		case strings.Contains(lower, query):
			contains = append(contains, choice{Name: l, Value: l})
		}
	}
	all := append(prefix, contains...)
	if len(all) > maxChoices {
		all = all[:maxChoices]
	}
	return all
}

package mention

import (
	"strings"
	"testing"
)

// FuzzExtract checks that arbitrary input never panics and that every result
// satisfies the package's output guarantees.
func FuzzExtract(f *testing.F) {
	seeds := []string{
		"", "@alice", "_@bob_", "@org/team", "```\n@x\n```", "<!-- @a", "> @q",
		"`` ` @a ``", "@" + strings.Repeat("a", 39) + "-", "é@x @y_", "\\@z @@w",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, text string) {
		got := Extract(text)

		seen := map[string]bool{}
		for _, u := range got.Users {
			assertValidLogin(t, u)
			if seen[u] {
				t.Fatalf("duplicate user %q", u)
			}
			seen[u] = true
		}
		for _, team := range got.Teams {
			org, slug, ok := strings.Cut(team, "/")
			if !ok || slug == "" {
				t.Fatalf("team %q is not org/slug", team)
			}
			assertValidLogin(t, org)
			if seen[team] {
				t.Fatalf("duplicate team %q", team)
			}
			seen[team] = true
		}

		if added := Added(text, text); !added.IsEmpty() {
			t.Fatalf("Added(x, x) = %+v, want empty", added)
		}
	})
}

func assertValidLogin(t *testing.T, login string) {
	t.Helper()
	if login == "" || len(login) > maxLoginLen {
		t.Fatalf("login %q has invalid length", login)
	}
	if login != strings.ToLower(login) {
		t.Fatalf("login %q is not lowercase", login)
	}
	if login[0] == '-' || login[len(login)-1] == '-' {
		t.Fatalf("login %q starts or ends with a hyphen", login)
	}
	for i := 0; i < len(login); i++ {
		if !isAlnum(login[i]) && login[i] != '-' {
			t.Fatalf("login %q contains invalid byte %q", login, login[i])
		}
	}
}

// Package mention extracts GitHub @user and @org/team mentions from Markdown
// text, following the same rules GitHub uses to decide who gets notified:
// mentions inside code, block quotes, HTML comments and email addresses are
// ignored.
package mention

import "strings"

// maxLoginLen is GitHub's maximum username (and organization name) length.
const maxLoginLen = 39

// Result holds the mentions found in a text, lowercased, deduplicated and in
// order of first appearance. Teams are formatted "org/team-slug".
type Result struct {
	Users []string
	Teams []string
}

// IsEmpty reports whether no users or teams were mentioned.
func (r Result) IsEmpty() bool {
	return len(r.Users) == 0 && len(r.Teams) == 0
}

// Extract returns all user and team mentions in text.
func Extract(text string) Result {
	var (
		v visibility
		c collector
	)
	for line := range strings.SplitSeq(text, "\n") {
		c.scan(v.visibleText(strings.TrimSuffix(line, "\r")))
	}
	return c.result()
}

// Added returns mentions present in after but not in before. It is used for
// edits so that only newly added mentions trigger a notification.
func Added(before, after string) Result {
	old := Extract(before)
	cur := Extract(after)
	return Result{
		Users: subtract(cur.Users, old.Users),
		Teams: subtract(cur.Teams, old.Teams),
	}
}

func subtract(items, remove []string) []string {
	drop := make(map[string]struct{}, len(remove))
	for _, r := range remove {
		drop[r] = struct{}{}
	}
	var out []string
	for _, it := range items {
		if _, ok := drop[it]; !ok {
			out = append(out, it)
		}
	}
	return out
}

// collector accumulates unique mentions in first-seen order.
type collector struct {
	users, teams []string
	seen         map[string]struct{}
}

func (c *collector) add(list *[]string, key string) {
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	*list = append(*list, key)
}

func (c *collector) result() Result {
	return Result{Users: c.users, Teams: c.teams}
}

// scan finds mentions in a single line of already-visible text.
func (c *collector) scan(s string) {
	for i := 0; i < len(s); i++ {
		if s[i] != '@' || !mentionCanStartAt(s, i) {
			continue
		}
		login, n := readLogin(s[i+1:])
		if login == "" {
			continue
		}
		end := i + 1 + n

		if end < len(s) && s[end] == '/' {
			if slug, m := readTeamSlug(s[end+1:]); slug != "" {
				c.add(&c.teams, strings.ToLower(login+"/"+slug))
				i = end + m
				continue
			}
		}
		if !mentionCanEndAt(s, end) {
			continue // e.g. "@alice_bob": not a valid GitHub login
		}
		c.add(&c.users, strings.ToLower(login))
		i = end - 1
	}
}

// readLogin reads a GitHub login at the start of s: alphanumerics and hyphens,
// not starting or ending with a hyphen, at most maxLoginLen characters. It
// returns the login and the number of bytes it occupies, or "" if invalid.
func readLogin(s string) (string, int) {
	if s == "" || !isAlnum(s[0]) {
		return "", 0
	}
	n := 0
	for n < len(s) && (isAlnum(s[n]) || s[n] == '-') {
		n++
	}
	login := strings.TrimRight(s[:n], "-")
	if len(login) > maxLoginLen {
		return "", 0
	}
	return login, len(login)
}

// readTeamSlug reads a team slug (alphanumerics, hyphens, underscores).
func readTeamSlug(s string) (string, int) {
	n := 0
	for n < len(s) && (isAlnum(s[n]) || s[n] == '-' || s[n] == '_') {
		n++
	}
	slug := strings.TrimRight(s[:n], "-")
	return slug, len(slug)
}

// mentionCanStartAt reports whether the '@' at s[at] may start a mention.
// Word characters (emails), path separators (URLs), backslashes (escapes) and
// another '@' directly before it prevent a mention. Underscores are allowed
// only as Markdown emphasis ("_@alice_"), not inside a word ("x_@alice").
func mentionCanStartAt(s string, at int) bool {
	i := at - 1
	for i >= 0 && s[i] == '_' {
		i--
	}
	if i < 0 {
		return true
	}
	c := s[i]
	if i < at-1 { // underscores were skipped: they must start an emphasis run
		return !isWordByte(c)
	}
	if isWordByte(c) {
		return false
	}
	switch c {
	case '@', '/', '\\', '.', '-', '`':
		return false
	}
	return true
}

// mentionCanEndAt reports whether a login ending at s[end] is a complete
// mention: it must not run into another word character, except through
// closing emphasis underscores ("_@alice_").
func mentionCanEndAt(s string, end int) bool {
	i := end
	for i < len(s) && s[i] == '_' {
		i++
	}
	if i == end {
		return end >= len(s) || !isWordByte(s[end])
	}
	return i >= len(s) || !isWordByte(s[i])
}

// isWordByte treats non-ASCII bytes as word characters so that "é@alice" is
// not a mention.
func isWordByte(c byte) bool {
	return isAlnum(c) || c == '_' || c >= 0x80
}

func isAlnum(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

package mention

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtract(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantUsers []string
		wantTeams []string
	}{
		// --- basics ---
		{name: "empty text", text: ""},
		{name: "no mentions", text: "just a normal comment"},
		{name: "single mention", text: "@alice please review", wantUsers: []string{"alice"}},
		{name: "mention mid sentence", text: "thanks @bob!", wantUsers: []string{"bob"}},
		{name: "multiple mentions keep first-seen order", text: "@carol and @alice and @bob", wantUsers: []string{"carol", "alice", "bob"}},
		{name: "duplicates removed case-insensitively", text: "@Alice @alice @ALICE", wantUsers: []string{"alice"}},
		{name: "hyphenated login", text: "cc @mona-lisa", wantUsers: []string{"mona-lisa"}},
		{name: "digits in login", text: "@dev42 @9lives", wantUsers: []string{"dev42", "9lives"}},
		{name: "multi-line text", text: "line one\n@alice\nline three @bob", wantUsers: []string{"alice", "bob"}},
		{name: "CRLF line endings", text: "hi\r\n@alice\r\n", wantUsers: []string{"alice"}},

		// --- surrounding punctuation ---
		{name: "trailing punctuation stripped", text: "@alice, @bob. @carol: @dave? @erin)", wantUsers: []string{"alice", "bob", "carol", "dave", "erin"}},
		{name: "wrapped in brackets and quotes", text: `(@alice) [@bob] "@carol" '@dave'`, wantUsers: []string{"alice", "bob", "carol", "dave"}},
		{name: "markdown emphasis", text: "**@alice** _@bob_ ~~@carol~~", wantUsers: []string{"alice", "bob", "carol"}},
		{name: "underscore emphasis at line start and end", text: "__@alice__", wantUsers: []string{"alice"}},
		{name: "underscore emphasis followed by punctuation", text: "_@alice_, hi", wantUsers: []string{"alice"}},
		{name: "trailing hyphen not part of login", text: "@alice- see", wantUsers: []string{"alice"}},
		{name: "start of markdown list item", text: "- @alice\n* @bob\n1. @carol", wantUsers: []string{"alice", "bob", "carol"}},

		// --- not mentions ---
		{name: "email address", text: "mail me at alice@example.com"},
		{name: "word directly before @", text: "foo@bar"},
		{name: "underscore before @", text: "x_@alice"},
		{name: "bare @", text: "meet @ 5pm"},
		{name: "@ followed by hyphen", text: "@-alice"},
		{name: "login too long", text: "@" + strings.Repeat("a", 40)},
		{name: "login at max length", text: "@" + strings.Repeat("a", 39), wantUsers: []string{strings.Repeat("a", 39)}},
		{name: "underscore after login invalidates it", text: "@alice_bob"},
		{name: "escaped @", text: `\@alice`},
		{name: "URL path", text: "https://example.com/@alice"},
		{name: "double @", text: "@@alice"},
		{name: "non-ascii letter before @", text: "é@alice"},
		{name: "non-ascii letter after login", text: "@aliceé"},
		{name: "non-ascii digit after login", text: "@alice٣"},

		// --- non-ASCII punctuation, symbols and scripts around mentions ---
		{name: "curly quotes", text: "“@alice” ‘@bob’", wantUsers: []string{"alice", "bob"}},
		{name: "em dash before", text: "thanks—@alice", wantUsers: []string{"alice"}},
		{name: "CJK full-width comma before and after", text: "你好，@alice，请看", wantUsers: []string{"alice"}},
		{name: "CJK text directly before", text: "请@alice看", wantUsers: nil},
		{name: "emoji before", text: "👋@alice", wantUsers: []string{"alice"}},
		{name: "curly apostrophe possessive", text: "@alice’s PR", wantUsers: []string{"alice"}},
		{name: "ellipsis after", text: "@alice…", wantUsers: []string{"alice"}},
		{name: "full-width colon after", text: "@alice：done", wantUsers: []string{"alice"}},
		{name: "non-breaking space before", text: "cc @alice", wantUsers: []string{"alice"}},

		// --- teams ---
		{name: "team mention", text: "cc @my-org/backend", wantTeams: []string{"my-org/backend"}},
		{name: "team mention lowercased and deduped", text: "@My-Org/Backend @my-org/backend", wantTeams: []string{"my-org/backend"}},
		{name: "team with underscore slug", text: "@org/web_team", wantTeams: []string{"org/web_team"}},
		{name: "users and teams together", text: "@alice @org/qa @bob", wantUsers: []string{"alice", "bob"}, wantTeams: []string{"org/qa"}},
		{name: "org followed by slash and nothing is a user", text: "@alice/ hi", wantUsers: []string{"alice"}},

		// --- code ---
		{name: "inline code ignored", text: "run `@alice` now @bob", wantUsers: []string{"bob"}},
		{name: "double-backtick inline code ignored", text: "``code with ` and @alice`` @bob", wantUsers: []string{"bob"}},
		{name: "unclosed backtick is literal", text: "a ` b @alice", wantUsers: []string{"alice"}},
		{name: "fenced code block ignored", text: "@alice\n```\n@bob\n```\n@carol", wantUsers: []string{"alice", "carol"}},
		{name: "fenced block with info string", text: "```go\n// @bob\n```\n@carol", wantUsers: []string{"carol"}},
		{name: "tilde fence ignored", text: "~~~\n@bob\n~~~\n@carol", wantUsers: []string{"carol"}},
		{name: "longer closing fence closes", text: "```\n@bob\n`````\n@carol", wantUsers: []string{"carol"}},
		{name: "shorter fence does not close", text: "````\n```\n@bob\n````\n@carol", wantUsers: []string{"carol"}},
		{name: "different fence char does not close", text: "```\n~~~\n@bob\n```\n@carol", wantUsers: []string{"carol"}},
		{name: "unclosed fence hides rest", text: "@alice\n```\n@bob", wantUsers: []string{"alice"}},
		{name: "indented fence up to 3 spaces", text: "   ```\n@bob\n   ```\n@carol", wantUsers: []string{"carol"}},

		// --- quotes ---
		{name: "quoted reply line ignored", text: "> @alice wrote this\n@bob I agree", wantUsers: []string{"bob"}},
		{name: "indented quote ignored", text: "  > @alice", wantUsers: nil},
		{name: "nested quote ignored", text: ">> @alice", wantUsers: nil},

		// --- HTML comments ---
		{name: "inline HTML comment ignored", text: "<!-- cc @alice --> @bob", wantUsers: []string{"bob"}},
		{name: "multi-line HTML comment ignored", text: "<!--\nTemplate: cc @alice\n-->\n@bob", wantUsers: []string{"bob"}},
		{name: "unclosed HTML comment hides rest", text: "@bob <!-- @alice\n@carol", wantUsers: []string{"bob"}},
		{name: "fence inside comment is not a fence", text: "<!--\n```\n-->\n@bob", wantUsers: []string{"bob"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.text)

			assertList(t, "Users", got.Users, tt.wantUsers)
			assertList(t, "Teams", got.Teams, tt.wantTeams)
		})
	}
}

func TestAdded(t *testing.T) {
	tests := []struct {
		name      string
		before    string
		after     string
		wantUsers []string
		wantTeams []string
	}{
		{name: "no change", before: "@alice", after: "@alice fixed typo"},
		{name: "new user added", before: "@alice", after: "@alice @bob", wantUsers: []string{"bob"}},
		{name: "case change is not new", before: "@alice", after: "@Alice"},
		{name: "removed mention is not reported", before: "@alice @bob", after: "@alice"},
		{name: "new team added", before: "hi", after: "hi @org/qa", wantTeams: []string{"org/qa"}},
		{name: "moved out of code block counts as new", before: "`@alice`", after: "@alice", wantUsers: []string{"alice"}},
		{name: "empty before means everything is new", before: "", after: "@alice @org/qa", wantUsers: []string{"alice"}, wantTeams: []string{"org/qa"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Added(tt.before, tt.after)

			assertList(t, "Users", got.Users, tt.wantUsers)
			assertList(t, "Teams", got.Teams, tt.wantTeams)
		})
	}
}

func TestResultIsEmpty(t *testing.T) {
	if !(Result{}).IsEmpty() {
		t.Fatal("zero Result should be empty")
	}
	if (Result{Users: []string{"a"}}).IsEmpty() {
		t.Fatal("Result with users should not be empty")
	}
	if (Result{Teams: []string{"o/t"}}).IsEmpty() {
		t.Fatal("Result with teams should not be empty")
	}
}

func assertList(t *testing.T, field string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", field, got, want)
	}
}

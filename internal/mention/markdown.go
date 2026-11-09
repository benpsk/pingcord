package mention

import "strings"

const (
	maxFenceIndent = 3 // CommonMark: fences and block quotes may be indented up to 3 spaces
	minFenceLen    = 3
	commentOpen    = "<!--"
	commentClose   = "-->"
)

// fence describes an open fenced code block.
type fence struct {
	char byte // '`' or '~'
	size int  // number of fence characters in the opening line
}

// visibility tracks block-level state across lines so that only text GitHub
// would render as prose (and therefore notify on) is scanned for mentions.
type visibility struct {
	open      *fence
	inComment bool
}

// visibleText returns the part of raw that may contain mentions: it drops
// fenced code blocks, block quotes, HTML comments and inline code spans.
func (v *visibility) visibleText(raw string) string {
	return stripInlineCode(v.blockText(raw))
}

func (v *visibility) blockText(raw string) string {
	if v.inComment {
		return v.stripComments(raw)
	}
	if v.open != nil {
		if v.open.closedBy(raw) {
			v.open = nil
		}
		return ""
	}
	if f, ok := openingFence(raw); ok {
		v.open = &f
		return ""
	}
	if isBlockQuote(raw) {
		return ""
	}
	return v.stripComments(raw)
}

// stripComments removes HTML comments, which may span several lines.
func (v *visibility) stripComments(line string) string {
	var b strings.Builder
	for line != "" {
		if v.inComment {
			end := strings.Index(line, commentClose)
			if end < 0 {
				break
			}
			line = line[end+len(commentClose):]
			v.inComment = false
			continue
		}
		start := strings.Index(line, commentOpen)
		if start < 0 {
			b.WriteString(line)
			break
		}
		b.WriteString(line[:start])
		b.WriteByte(' ')
		line = line[start+len(commentOpen):]
		v.inComment = true
	}
	return b.String()
}

func openingFence(line string) (fence, bool) {
	rest, ok := trimBlockIndent(line)
	if !ok || rest == "" || (rest[0] != '`' && rest[0] != '~') {
		return fence{}, false
	}
	char := rest[0]
	size := countLeading(rest, char)
	if size < minFenceLen {
		return fence{}, false
	}
	// A backtick fence's info string may not contain backticks; otherwise
	// the line is inline code, not a fence.
	if char == '`' && strings.IndexByte(rest[size:], '`') >= 0 {
		return fence{}, false
	}
	return fence{char: char, size: size}, true
}

func (f fence) closedBy(line string) bool {
	rest, ok := trimBlockIndent(line)
	if !ok {
		return false
	}
	size := countLeading(rest, f.char)
	return size >= f.size && strings.TrimSpace(rest[size:]) == ""
}

func isBlockQuote(line string) bool {
	rest, ok := trimBlockIndent(line)
	return ok && strings.HasPrefix(rest, ">")
}

// trimBlockIndent strips up to maxFenceIndent leading spaces. It reports false
// when the line is indented further (an indented code block in CommonMark).
func trimBlockIndent(line string) (string, bool) {
	n := countLeading(line, ' ')
	if n > maxFenceIndent {
		return "", false
	}
	return line[n:], true
}

func countLeading(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// stripInlineCode replaces `code spans` with a space. A backtick run only opens
// a span when a later run of exactly the same length closes it.
func stripInlineCode(line string) string {
	if strings.IndexByte(line, '`') < 0 {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		size := countLeading(line[i:], '`')
		end := findBacktickRun(line, i+size, size)
		if end < 0 {
			b.WriteString(line[i : i+size]) // unmatched: literal backticks
			i += size
			continue
		}
		b.WriteByte(' ')
		i = end + size
	}
	return b.String()
}

// findBacktickRun returns the index of the next run of exactly size backticks
// at or after from, or -1.
func findBacktickRun(s string, from, size int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := countLeading(s[i:], '`')
		if run == size {
			return i
		}
		i += run
	}
	return -1
}

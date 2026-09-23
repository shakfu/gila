package permission

import (
	"fmt"
	"strings"
)

// words splits a command into words as bash would for one simple command, handling single
// and double quotes. It reports false for anything that could run a second program, expand a
// substitution or redirect: an unquoted ; & | < > ( ) $ ` \ or newline, a $ ` or \ inside
// double quotes, or an unbalanced quote. Such a command is never matched by an allowlist
// entry, so it asks.
func words(cmd string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			end := strings.IndexByte(cmd[i+1:], '"')
			if end < 0 {
				return nil, false
			}
			inner := cmd[i+1 : i+1+end]
			if strings.ContainsAny(inner, "$`\\") {
				return nil, false
			}
			cur.WriteString(inner)
			i += end + 1
			inWord = true
		case strings.IndexByte(";&|<>()$`\\\n\r", c) >= 0:
			return nil, false
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, len(out) > 0
}

// allowed reports whether a command is one simple command that an entry allows. An entry is a
// word prefix, so "go test" allows "go test ./..."; one ending in "$" must match the whole
// command, so "git status$" allows "git status" and nothing longer.
func allowed(entries []string, cmd string) bool {
	got, ok := words(cmd)
	if !ok {
		return false
	}
	for _, e := range entries {
		want, exact := entry(e)
		if len(want) == 0 || len(want) > len(got) || exact && len(want) != len(got) {
			continue
		}
		if equalWords(want, got[:len(want)]) {
			return true
		}
	}
	return false
}

// entry splits an allowlist entry into its words and whether it is exact.
func entry(e string) ([]string, bool) {
	body, exact := strings.CutSuffix(strings.TrimSpace(e), "$")
	w, _ := words(body)
	return w, exact
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkCommand(e string) error {
	body, _ := strings.CutSuffix(strings.TrimSpace(e), "$")
	if _, ok := words(body); !ok {
		return fmt.Errorf("bad command %q: an entry is plain words, such as \"go test\", "+
			"optionally ending in $ to match exactly", e)
	}
	return nil
}

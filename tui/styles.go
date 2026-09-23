package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gila/agent"
	"github.com/shakfu/gila/app"
	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/price"
)

// Styles is the palette. Output is ASCII; colour carries the structure.
type Styles struct {
	Banner, Dim, Accent, Model, Error, Warn, OK, Cost    lipgloss.Style
	User                                                 lipgloss.Style
	Tool                                                 map[string]lipgloss.Style
	Heading, Bold, Italic, Code, CodeBlock, Link, Quote  lipgloss.Style
	BarLeft, BarModel, BarCtx, BarCost, BarFill, BarBusy lipgloss.Style
	Selected, Match                                      lipgloss.Style
}

// Writer downsamples colour for w, the way the REPL's renderer does for the terminal: a pipe
// gets plain text. With color off a terminal keeps bold and italic and drops colour.
func Writer(w io.Writer, color bool) io.Writer {
	cw := colorprofile.NewWriter(w, os.Environ())
	if !color && cw.Profile > colorprofile.Ascii {
		cw.Profile = colorprofile.Ascii
	}
	return cw
}

// NewStyles builds the palette. Colour is downsampled where it is written, not here.
func NewStyles() Styles {
	s := func(fg string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(fg)) }
	bar := func(fg, bg string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg)).Padding(0, 1)
	}
	return Styles{
		Banner: s("81").Bold(true),
		Dim:    s("245"),
		Accent: s("81"),
		Model:  s("177"),
		Error:  s("203"),
		Warn:   s("221"),
		OK:     s("114"),
		Cost:   s("150"),
		User:   s("252").Bold(true),
		Tool: map[string]lipgloss.Style{
			"read":  s("75").Bold(true),
			"write": s("114").Bold(true),
			"edit":  s("221").Bold(true),
			"bash":  s("209").Bold(true),
		},
		Heading:   s("81").Bold(true),
		Bold:      lipgloss.NewStyle().Bold(true),
		Italic:    lipgloss.NewStyle().Italic(true),
		Code:      s("216"),
		CodeBlock: s("180"),
		Link:      s("75").Underline(true),
		Quote:     s("245").Italic(true),
		BarLeft:   bar("252", "237"),
		BarModel:  bar("231", "97").Bold(true),
		BarCtx:    bar("231", "31"),
		BarCost:   bar("16", "150"),
		BarFill:   lipgloss.NewStyle().Background(lipgloss.Color("236")),
		BarBusy:   bar("16", "221").Bold(true),
		Selected:  s("231").Background(lipgloss.Color("97")).Bold(true),
		Match:     s("252"),
	}
}

// toolPrefix marks a tool line, as in myra, so it stands apart from the answer when the
// transcript is read without colour.
const toolPrefix = "[tool] "

// ToolLine formats a finished call as one line: "[tool] ", its label, then the outcome. width
// 0 means no limit. When the line is too wide the label is cut first, so the outcome stays.
func ToolLine(st Styles, r agent.ToolResult, width int) string {
	name := r.Call.Name
	style, ok := st.Tool[name]
	if !ok {
		style = st.Accent.Bold(true)
	}
	label := r.Label
	head, rest, _ := strings.Cut(label, " ")
	if name == "bash" {
		head, rest = "$", strings.TrimPrefix(label, "$ ")
	}
	var outcome string
	switch {
	case r.Err != nil:
		outcome = st.Error.Render("error: " + oneLine(ansi.Strip(firstLine(r.Err.Error()))))
	case r.Result.Failed:
		outcome = st.Warn.Render(r.Result.Summary)
	default:
		outcome = st.Dim.Render(r.Result.Summary)
	}
	rest = oneLine(ansi.Strip(rest))
	build := func(rest string) string {
		line := st.Dim.Render(toolPrefix) + style.Render(head)
		if rest != "" {
			line += " " + rest
		}
		return line + st.Dim.Render(" -> ") + outcome
	}
	line := build(rest)
	if width > 0 && ansi.StringWidth(line) > width {
		fixed := len(toolPrefix) + ansi.StringWidth(head) + len(" ") + len(" -> ") + ansi.StringWidth(outcome)
		if room := width - fixed; room > 8 && rest != "" {
			line = build(ansi.Truncate(rest, room, "..."))
		}
		if r.Err == nil {
			line = ansi.Truncate(line, width, "...")
		}
	}
	return line
}

// RetryLine describes a retry, such as "[retry] 2 after 503 Service Unavailable".
func RetryLine(r agent.Retry) string {
	line := fmt.Sprintf("[retry] %d", r.Attempt)
	if r.Reason != "" {
		line += " after " + oneLine(r.Reason)
	}
	return line
}

// UsageLine summarises a prompt: context used, tokens in and out, cost.
func UsageLine(a *app.App, u llm.Usage) string {
	return usageLine(a.Agent.Used, a.Agent.Context, u, a.Agent.Usage)
}

func usageLine(used, window int64, u, session llm.Usage) string {
	var parts []string
	if window > 0 {
		parts = append(parts, fmt.Sprintf("ctx %s/%s (%d%%)", price.Short(used), price.Short(window), used*100/window))
	} else {
		parts = append(parts, "ctx "+price.Short(used))
	}
	in := "in " + price.Short(u.Input)
	if u.CacheRead > 0 {
		in += fmt.Sprintf(" (%s cached)", price.Short(u.CacheRead))
	}
	parts = append(parts, in, "out "+price.Short(u.Output))
	if u.Cost != nil {
		c := money(u.Cost, u.Estimated)
		if session.Cost != nil && *session.Cost != *u.Cost {
			c += " (session " + money(session.Cost, session.Estimated) + ")"
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, " | ")
}

// money formats USD, marking an estimate with ~.
func money(c *float64, estimated bool) string {
	if c == nil {
		return "$-"
	}
	s := fmt.Sprintf("$%.4f", *c)
	if *c >= 1 {
		s = fmt.Sprintf("$%.2f", *c)
	}
	if estimated {
		s = "~" + s
	}
	return s
}

// shortPath replaces the home directory with ~.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// oneLine keeps the first line of s and drops control characters, which would move the
// cursor in the inline REPL.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

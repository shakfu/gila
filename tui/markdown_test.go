package tui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gila/agent"
	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/tool"
)

func TestMarkdownLines(t *testing.T) {
	md := markdown{st: NewStyles()}
	cases := []struct{ in, want string }{
		{"## Title **x**", "## Title x"},
		{"- item with `code`", "- item with code"},
		{"* star item", "- star item"},
		{"1. first", "1. first"},
		{"**bold** and *it* and _it_", "bold and it and it"},
		{"see [docs](https://x.y)", "see docs (https://x.y)"},
		{"> quoted", "| quoted"},
		{"---", "----------------------------------------"},
		{"a * b * c", "a * b * c"},
		{"snake_case_name", "snake_case_name"},
		{"`**not bold**`", "**not bold**"},
	}
	for _, c := range cases {
		if got := ansi.Strip(md.line(c.in)); got != c.want {
			t.Errorf("line(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFencesAreNotStyledInside(t *testing.T) {
	md := markdown{st: NewStyles()}
	lines := []string{"```go", "# not a heading", "- not a bullet", "```", "- bullet"}
	var got []string
	for _, l := range lines {
		got = append(got, ansi.Strip(md.line(l)))
	}
	want := []string{"```go", "# not a heading", "- not a bullet", "```", "- bullet"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: %q, want %q", i, got[i], want[i])
		}
	}
	if md.inFence {
		t.Fatal("fence left open")
	}
}

func TestUsageLine(t *testing.T) {
	c := 0.0123
	s := 0.05
	got := usageLine(1500, 200000, llmUsage(1000, 400, 50, &c, true), llmUsage(0, 0, 0, &s, true))
	want := "ctx 1.5k/200k (0%) | in 1k (400 cached) | out 50 | ~$0.0123 (session ~$0.0500)"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func llmUsage(in, cached, out int64, cost *float64, est bool) llm.Usage {
	return llm.Usage{Input: in, CacheRead: cached, Output: out, Cost: cost, Estimated: est}
}

func TestToolLine(t *testing.T) {
	st := NewStyles()
	res := func(name, label, summary string, err error) agent.ToolResult {
		return agent.ToolResult{Call: llm.ToolCall{Name: name}, Label: label, Result: tool.Result{Summary: summary}, Err: err}
	}
	cases := []struct {
		r     agent.ToolResult
		width int
		want  string
	}{
		{res("read", "read main.go:1-80", "80 lines", nil), 0, "[tool] read main.go:1-80 -> 80 lines"},
		{res("bash", "$ go test ./...", "exit 1: FAIL", nil), 0, "[tool] $ go test ./... -> exit 1: FAIL"},
		{res("deploy", "deploy", "done", nil), 0, "[tool] deploy -> done"},
		{res("bash", "$ sed -n '1,300p' agent/agent.go && sed -n '1,260p' agent/record.go", "310 lines", nil), 50,
			"[tool] $ sed -n '1,300p' agent/age... -> 310 lines"},
		{res("write", "write /etc/x", "", errors.New("refused: auto mode asks before write outside /r")), 30,
			"[tool] write /etc/x -> error: refused: auto mode asks before write outside /r"},
	}
	for _, c := range cases {
		got := ansi.Strip(ToolLine(st, c.r, c.width))
		if got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
		if c.width > 0 && c.r.Err == nil && ansi.StringWidth(got) > c.width {
			t.Errorf("%q is wider than %d", got, c.width)
		}
	}
}

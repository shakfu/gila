package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/shakfu/gilda/llm"
)

type Edit struct{ Env }

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (Edit) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "edit",
		Description: "Replace exact text in a file. old_string must occur exactly once unless replace_all is true.",
		Schema: schema([]string{"path", "old_string", "new_string"}, map[string]any{
			"path":        prop("string", "File path, absolute or relative to the working directory."),
			"old_string":  prop("string", "Exact text to replace, without read's line-number prefixes."),
			"new_string":  prop("string", "Replacement text."),
			"replace_all": prop("boolean", "Replace every occurrence."),
		}),
	}
}

func (Edit) Label(raw json.RawMessage) string {
	var a editArgs
	_ = decode(raw, &a)
	return "edit " + a.Path
}

func (e Edit) Run(_ context.Context, raw json.RawMessage) (Result, error) {
	c, err := e.apply(raw)
	if err != nil {
		return Result{}, err
	}
	if err := replace(c.path, []byte(c.after)); err != nil {
		return Result{}, err
	}
	summary := "1 replacement"
	if c.n > 1 {
		summary = fmt.Sprintf("%d replacements", c.n)
	}
	return Result{Output: fmt.Sprintf("edited %s: %s", c.name, summary), Summary: summary}, nil
}

// Preview returns the unified diff Run would apply, computed the same way.
func (e Edit) Preview(raw json.RawMessage) (string, error) {
	c, err := e.apply(raw)
	if err != nil {
		return "", err
	}
	return udiff.Unified(c.name, c.name, c.before, c.after), nil
}

type change struct {
	path, name, before, after string
	n                         int
}

// apply works out an edit without writing it. It refuses an ambiguous match: an edit that
// hits the wrong occurrence is worse than a failed one.
func (e Edit) apply(raw json.RawMessage) (change, error) {
	var a editArgs
	if err := decode(raw, &a, "path", "old_string", "new_string"); err != nil {
		return change{}, err
	}
	if a.Path == "" {
		return change{}, fmt.Errorf("path is required")
	}
	// An empty pattern matches between every character.
	if a.OldString == "" {
		return change{}, fmt.Errorf("old_string is empty; use write to create a file")
	}
	if a.OldString == a.NewString {
		return change{}, fmt.Errorf("old_string and new_string are identical")
	}
	path := e.abs(a.Path)
	f, _, err := openRegular(path, a.Path)
	if err != nil {
		return change{}, err
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return change{}, err
	}
	text := string(data)
	old, repl := a.OldString, a.NewString
	// read shows lines without their \r, so a multi-line old_string copied from it cannot match
	// a CRLF file as given.
	if strings.Contains(old, "\n") && !strings.Contains(text, old) && strings.Contains(text, "\r\n") {
		old = strings.ReplaceAll(old, "\n", "\r\n")
		repl = strings.ReplaceAll(repl, "\n", "\r\n")
	}
	n := strings.Count(text, old)
	switch {
	case n == 0:
		return change{}, fmt.Errorf("old_string not found in %s", a.Path)
	case n > 1 && !a.ReplaceAll:
		return change{}, fmt.Errorf("old_string occurs %d times in %s; add context or set replace_all", n, a.Path)
	}
	c := change{path: path, name: a.Path, before: text, n: n}
	if a.ReplaceAll {
		c.after = strings.ReplaceAll(text, old, repl)
	} else {
		c.after = strings.Replace(text, old, repl, 1)
	}
	return c, nil
}

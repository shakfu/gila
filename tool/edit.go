package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shakfu/gila/llm"
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

// Run refuses an ambiguous match: an edit that hits the wrong occurrence is worse than a
// failed one.
func (e Edit) Run(_ context.Context, raw json.RawMessage) (Result, error) {
	var a editArgs
	if err := decode(raw, &a); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	// An empty pattern matches between every character.
	if a.OldString == "" {
		return Result{}, fmt.Errorf("old_string is empty; use write to create a file")
	}
	if a.OldString == a.NewString {
		return Result{}, fmt.Errorf("old_string and new_string are identical")
	}
	path := e.abs(a.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
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
		return Result{}, fmt.Errorf("old_string not found in %s", a.Path)
	case n > 1 && !a.ReplaceAll:
		return Result{}, fmt.Errorf("old_string occurs %d times in %s; add context or set replace_all", n, a.Path)
	}
	if a.ReplaceAll {
		text = strings.ReplaceAll(text, old, repl)
	} else {
		text = strings.Replace(text, old, repl, 1)
	}
	if err := replace(path, []byte(text)); err != nil {
		return Result{}, err
	}
	summary := "1 replacement"
	if n > 1 {
		summary = fmt.Sprintf("%d replacements", n)
	}
	return Result{Output: fmt.Sprintf("edited %s: %s", a.Path, summary), Summary: summary}, nil
}

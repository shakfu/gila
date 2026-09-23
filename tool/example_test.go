package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shakfu/gila/tool"
)

// A read-only tool runs without asking in every permission mode; a modifying tool that names
// its paths runs without asking in auto mode while they stay inside the working directory.
func ExampleNew() {
	env := tool.Env{Root: "."}
	count, _ := tool.New(tool.Def{
		Name:        "count",
		Description: "Count the lines of a file.",
		Schema: map[string]any{"type": "object", "required": []string{"path"},
			"properties": map[string]any{"path": map[string]any{"type": "string"}}},
		ReadOnly: true,
		Paths: func(args json.RawMessage) ([]string, error) {
			var a struct{ Path string }
			err := json.Unmarshal(args, &a)
			return []string{a.Path}, err
		},
		Run: func(_ context.Context, args json.RawMessage) (tool.Result, error) {
			var a struct{ Path string }
			if err := json.Unmarshal(args, &a); err != nil {
				return tool.Result{}, err
			}
			data, err := os.ReadFile(env.Abs(a.Path))
			if err != nil {
				return tool.Result{}, err
			}
			n := strings.Count(string(data), "\n")
			return tool.Result{Output: fmt.Sprint(n), Summary: fmt.Sprintf("%d lines", n)}, nil
		},
	})
	_, readOnly := count.(tool.ReadOnly)
	_, paths := count.(tool.Paths)
	fmt.Println(count.Spec().Name, readOnly, paths)
	// Output: count true true
}

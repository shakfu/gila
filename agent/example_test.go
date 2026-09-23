package agent_test

import (
	"context"
	"fmt"
	"os"

	"github.com/shakfu/gila/agent"
	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/llm/mock"
	"github.com/shakfu/gila/prompt"
	"github.com/shakfu/gila/tool"
)

// A real provider takes the mock's place, e.g. anthropic.New("anthropic", "", "").
func Example() {
	dir, _ := os.MkdirTemp("", "gila-example")
	defer os.RemoveAll(dir)

	a := agent.New(agent.Config{
		Provider: mock.New(
			mock.Step{Calls: []mock.Call{{Name: "bash", Arguments: []byte(`{"command":"echo 42"}`)}}},
			mock.Step{Text: "The answer is 42.", Usage: llm.Usage{Input: 120, Output: 8}},
		),
		Model:  "claude-opus-5",
		System: prompt.Build(dir, ""),
		Tools:  tool.Default(tool.Env{Root: dir, Jobs: &tool.Jobs{}}),
	})
	res, err := a.Run(context.Background(), "what does echo 42 print?", func(e agent.Event) {
		if r, ok := e.(agent.ToolResult); ok {
			fmt.Println(r.Label, "->", r.Result.Summary)
		}
	})
	fmt.Println(res.Text, res.Turns, err)
	// Output:
	// $ echo 42 -> 42
	// The answer is 42. 2 <nil>
}

package openrouter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OpenRouterTeam/go-sdk/models/components"

	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/llm/llmtest"
)

var turn = llmtest.SSE(
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"th","reasoning_details":[{"type":"reasoning.text","text":"th","index":0}]},"finish_reason":null}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{"reasoning":"ink","reasoning_details":[{"type":"reasoning.text","text":"ink","signature":"SIG","index":0}]},"finish_reason":null}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]},"finish_reason":null}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	"", `{"id":"g1","object":"chat.completion.chunk","created":1,"model":"anthropic/claude-x","choices":[],"usage":{"prompt_tokens":200,"completion_tokens":30,"total_tokens":230,"cost":0.0042,"prompt_tokens_details":{"cached_tokens":150,"cache_write_tokens":20}}}`,
	"", `[DONE]`,
)

var answer = llmtest.SSE(
	"", `{"id":"g2","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	"", `[DONE]`,
)

func request(model string, history ...llm.Message) llm.Request {
	return llm.Request{Model: model, System: "sys", Messages: history, MaxTokens: 100, SessionID: "sess", Effort: "medium",
		Tools: []llm.ToolSpec{{Name: "read", Description: "d", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}}
}

func TestStreamReportsCostAndMergesReasoning(t *testing.T) {
	srv := llmtest.New(t, turn, answer)
	p := New("openrouter", "k", srv.URL)
	user := llm.Message{Role: llm.User, Text: "hi"}
	resp, err := p.Stream(context.Background(), request("anthropic/claude-x", user), func(llm.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Message
	if m.Text != "Hi" || len(m.Calls) != 1 || m.Calls[0].Arguments != `{"path":"a"}` || resp.Stop != llm.StopToolUse {
		t.Fatalf("message %+v", m)
	}
	u := resp.Usage
	if u.Cost == nil || *u.Cost != 0.0042 || u.Estimated || u.CacheRead != 150 || u.CacheWrite != 20 || u.Input != 200 {
		t.Fatalf("usage %+v", u)
	}

	body := srv.Bodies[0]
	checks := map[string]bool{
		"cache_control":  llmtest.Get(body, "cache_control", "type") == "ephemeral",
		"session_id":     llmtest.Get(body, "session_id") == "sess",
		"stream":         llmtest.Get(body, "stream") == true,
		"include usage":  llmtest.Get(body, "stream_options", "include_usage") == true,
		"effort":         llmtest.Get(body, "reasoning", "effort") == "medium",
		"system message": llmtest.Get(body, "messages", 0, "role") == "system",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("request: %s (%v)", name, body)
		}
	}

	// Streamed fragments of one reasoning entry replay as one entry, signature included.
	history := []llm.Message{user, m, {Role: llm.Tool, Results: []llm.ToolResult{{CallID: "call_1", Content: "x"}}}}
	if _, err := p.Stream(context.Background(), request("anthropic/claude-x", history...), func(llm.Event) {}); err != nil {
		t.Fatal(err)
	}
	details := llmtest.Get(srv.Bodies[1], "messages", 2, "reasoning_details")
	list, _ := details.([]any)
	if len(list) != 1 || llmtest.Get(list, 0, "text") != "think" || llmtest.Get(list, 0, "signature") != "SIG" {
		t.Fatalf("replayed reasoning %v", details)
	}
	if llmtest.Get(srv.Bodies[1], "messages", 3, "tool_call_id") != "call_1" {
		t.Fatalf("tool result %v", llmtest.Get(srv.Bodies[1], "messages", 3))
	}
}

func TestASignatureClosesAReasoningEntry(t *testing.T) {
	var a accumulator
	chunk := func(data string) {
		srvChunk(t, &a, data)
	}
	chunk(`{"id":"g","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"one","signature":"S1"}]}}]}`)
	chunk(`{"id":"g","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"two","signature":"S2"}]}}]}`)
	if len(a.reasoning) != 2 {
		t.Fatalf("merged signed entries: %d", len(a.reasoning))
	}
}

func srvChunk(t *testing.T, a *accumulator, data string) {
	t.Helper()
	var c components.ChatStreamChunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		t.Fatal(err)
	}
	a.add(c, func(llm.Event) {})
}

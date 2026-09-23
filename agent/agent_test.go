package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/llm/mock"
	"github.com/shakfu/gila/price"
	"github.com/shakfu/gila/tool"
)

func newAgent(t *testing.T, steps ...mock.Step) (*Agent, *mock.Provider, string) {
	t.Helper()
	root := t.TempDir()
	p := mock.New(steps...)
	a := New(Config{Provider: p, Model: "m", Tools: tool.Default(tool.Env{Root: root, Jobs: &tool.Jobs{}})})
	return a, p, root
}

func call(name string, args any) mock.Call {
	data, _ := json.Marshal(args)
	return mock.Call{Name: name, Arguments: data}
}

func usage(in, out int64, cost float64) llm.Usage {
	return llm.Usage{Input: in, Output: out, Cost: llm.Float(cost)}
}

func TestRunLoopsThroughToolsToAnAnswer(t *testing.T) {
	a, p, root := newAgent(t,
		mock.Step{Calls: []mock.Call{call("write", map[string]string{"path": "x.txt", "content": "hi"})}, Usage: usage(100, 10, 0.01)},
		mock.Step{Text: "done", Usage: usage(150, 5, 0.02)},
	)
	var events []Event
	res, err := a.Run(context.Background(), "make x", func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || res.Turns != 2 {
		t.Fatalf("result %+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(data) != "hi" {
		t.Fatalf("file %q", data)
	}
	if *res.Usage.Cost != 0.03 || res.Usage.Input != 250 || a.Used != 155 {
		t.Fatalf("usage %+v used %d", res.Usage, a.Used)
	}

	// user, assistant with a call, tool results, assistant answer.
	h := a.History
	if len(h) != 4 || h[0].Role != llm.User || h[2].Role != llm.Tool || h[3].Text != "done" {
		t.Fatalf("history %+v", h)
	}
	if h[2].Results[0].CallID != h[1].Calls[0].ID || h[2].Results[0].IsError {
		t.Fatalf("result does not answer the call: %+v", h[2])
	}
	// The second request carried the whole history.
	if len(p.Requests[1].Messages) != 3 {
		t.Fatalf("second request had %d messages", len(p.Requests[1].Messages))
	}

	var kinds []string
	for _, e := range events {
		switch e.(type) {
		case ToolCall:
			kinds = append(kinds, "call")
		case ToolResult:
			kinds = append(kinds, "result")
		case Response:
			kinds = append(kinds, "response")
		}
	}
	if want := []string{"response", "call", "result", "response"}; !equal(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
}

func TestToolErrorsGoBackToTheModel(t *testing.T) {
	a, _, _ := newAgent(t,
		mock.Step{Calls: []mock.Call{call("read", map[string]string{"path": "missing"}), call("nope", nil)}},
		mock.Step{Text: "ok"},
	)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	results := a.History[2].Results
	if len(results) != 2 || !results[0].IsError || !results[1].IsError {
		t.Fatalf("results %+v", results)
	}
	if results[1].Content != `unknown tool "nope"` {
		t.Fatalf("got %q", results[1].Content)
	}
}

// A response cut off at max_tokens may carry truncated arguments, so its calls are answered
// with an error instead of run.
func TestTruncatedCallsAreNotRun(t *testing.T) {
	a, _, root := newAgent(t,
		mock.Step{Calls: []mock.Call{call("write", map[string]string{"path": "x", "content": "partial"})}, Stop: llm.StopMaxTokens},
		mock.Step{Text: "ok"},
	)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("a truncated call ran")
	}
	if r := a.History[2].Results[0]; !r.IsError {
		t.Fatalf("got %+v", r)
	}
}

// A refused response may carry calls; they are answered, not run, and the run fails.
func TestRefusedCallsAreNotRun(t *testing.T) {
	a, _, root := newAgent(t,
		mock.Step{Calls: []mock.Call{call("write", map[string]string{"path": "x", "content": "partial"})}, Stop: llm.StopRefusal},
	)
	if _, err := a.Run(context.Background(), "go", nil); err == nil {
		t.Fatal("a refusal was taken as success")
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("a refused call ran")
	}
	if r := a.History[2].Results[0]; !r.IsError {
		t.Fatalf("got %+v", r)
	}
}

func TestAFailedFirstRequestTakesThePromptBack(t *testing.T) {
	a, _, _ := newAgent(t, mock.Step{Error: "boom"})
	if _, err := a.Run(context.Background(), "go", nil); err == nil {
		t.Fatal("no error")
	}
	if len(a.History) != 0 {
		t.Fatalf("history kept %d messages", len(a.History))
	}
}

// A cancel during tools must still answer every call, or the next request is invalid.
func TestCancelAnswersEveryCall(t *testing.T) {
	a, _, _ := newAgent(t, mock.Step{Calls: []mock.Call{
		call("bash", map[string]string{"command": "sleep 5"}),
		call("read", map[string]string{"path": "x"}),
	}})
	ctx, cancel := context.WithCancel(context.Background())
	_, err := a.Run(ctx, "go", func(e Event) {
		if _, ok := e.(ToolCall); ok {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	last := a.History[len(a.History)-1]
	if last.Role != llm.Tool || len(last.Results) != 2 {
		t.Fatalf("last message %+v", last)
	}
	for _, r := range last.Results {
		if r.Content != Cancelled || !r.IsError {
			t.Fatalf("result %+v", r)
		}
	}
}

func TestMaxTurnsStopsTheLoop(t *testing.T) {
	step := mock.Step{Calls: []mock.Call{call("read", map[string]string{"path": "."})}}
	a, _, _ := newAgent(t, step, step, step)
	a.MaxTurns = 2
	res, err := a.Run(context.Background(), "go", nil)
	if err == nil || res.Turns != 2 {
		t.Fatalf("turns %d err %v", res.Turns, err)
	}
}

func TestAFullContextIsRefusedBeforeSending(t *testing.T) {
	a, p, _ := newAgent(t, mock.Step{Text: "a", Usage: usage(96, 0, 0)}, mock.Step{Text: "b"})
	a.Context = 100
	if _, err := a.Run(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "two", nil); !errors.Is(err, ErrContextFull) {
		t.Fatalf("err %v", err)
	}
	if len(p.Requests) != 1 || len(a.History) != 2 {
		t.Fatalf("requests %d history %d", len(p.Requests), len(a.History))
	}
	a.Reset()
	if _, err := a.Run(context.Background(), "three", nil); err != nil {
		t.Fatalf("after reset: %v", err)
	}
}

func TestCostIsEstimatedWhenTheProviderReportsNone(t *testing.T) {
	a, _, _ := newAgent(t, mock.Step{Text: "a", Usage: llm.Usage{Input: 1000, Output: 100, CacheRead: 400}})
	a.Prices = &price.Catalog{Models: map[string]price.Entry{
		"m": {Rates: &price.Rates{Prompt: 1e-6, Completion: 1e-5, CacheRead: 1e-7}},
	}}
	// The mock provider's name is "mock", which the catalog does not mirror.
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if a.Usage.Cost != nil {
		t.Fatal("estimated a cost for a provider the catalog does not list")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Anthropic rejects an empty assistant message on replay, so an empty reply must not enter the
// history as one.
func TestAnEmptyReplyIsRecordedWithText(t *testing.T) {
	a, _, _ := newAgent(t, mock.Step{}, mock.Step{Text: "next"})
	if _, err := a.Run(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	if m := a.History[1]; m.Text == "" || m.Native != nil {
		t.Fatalf("empty reply recorded as %+v", m)
	}
}

// The turns a prompt already completed stay when the window fills mid-prompt: their tools have
// changed files the model must remember.
func TestAFullContextMidPromptKeepsCompletedTurns(t *testing.T) {
	a, _, _ := newAgent(t, mock.Step{Calls: []mock.Call{call("read", map[string]string{"path": "."})}, Usage: usage(96, 0, 0)})
	a.Context = 100
	if _, err := a.Run(context.Background(), "go", nil); !errors.Is(err, ErrContextFull) {
		t.Fatalf("err %v", err)
	}
	if len(a.History) != 3 || a.History[2].Role != llm.Tool {
		t.Fatalf("history %+v", a.History)
	}
	if !ContextFull(err2(llm.ErrContext)) {
		t.Fatal("a provider's context error is not recognised")
	}
}

func err2(e error) error { return errors.Join(errors.New("400"), e) }

func TestApproveGatesEachCall(t *testing.T) {
	a, _, root := newAgent(t,
		mock.Step{Calls: []mock.Call{
			call("write", map[string]string{"path": "yes.txt", "content": "1"}),
			call("write", map[string]string{"path": "no.txt", "content": "2"}),
			call("bash", map[string]string{"command": "echo hi"}),
		}},
		mock.Step{Text: "ok"},
	)
	var asked []string
	a.Approve = func(_ context.Context, _ tool.Tool, c llm.ToolCall, label string) (bool, error) {
		asked = append(asked, label)
		switch label {
		case "write no.txt":
			return false, nil
		case "$ echo hi":
			return false, errors.New("dialog closed")
		}
		return true, nil
	}
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 {
		t.Fatalf("asked %v", asked)
	}
	if _, err := os.Stat(filepath.Join(root, "yes.txt")); err != nil {
		t.Fatal("an approved call did not run")
	}
	if _, err := os.Stat(filepath.Join(root, "no.txt")); err == nil {
		t.Fatal("a declined call ran")
	}
	r := a.History[2].Results
	if r[0].IsError || r[1].Content != Declined || !r[1].IsError || !strings.Contains(r[2].Content, "dialog closed") {
		t.Fatalf("results %+v", r)
	}
}

func TestRecordsAreJSONReady(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{Text{"hi"}, `{"text":"hi","type":"text"}`},
		{Retry{Attempt: 2, Reason: "529 status code 529"}, `{"attempt":2,"reason":"529 status code 529","type":"retry"}`},
		{ToolCall{Call: llm.ToolCall{ID: "1", Name: "read", Arguments: `{"path":"a"}`}, Label: "read a"},
			`{"arguments":{"path":"a"},"id":"1","label":"read a","name":"read","type":"tool_call"}`},
		{ToolCall{Call: llm.ToolCall{ID: "2", Name: "read", Arguments: `{"pa`}},
			`{"arguments":"{\"pa","id":"2","label":"","name":"read","type":"tool_call"}`},
		{ToolResult{Call: llm.ToolCall{ID: "1", Name: "read"}, Label: "read a", Err: errors.New("no such file")},
			`{"error":"no such file","id":"1","label":"read a","name":"read","ok":false,"output":"","summary":"","type":"tool_result"}`},
	}
	for _, c := range cases {
		data, err := json.Marshal(Record(c.ev))
		if err != nil || string(data) != c.want {
			t.Errorf("got  %s\nwant %s (%v)", data, c.want, err)
		}
	}
}

// A stream cut off mid-response is sent again, up to maxCut times in a row, without leaving
// the cut response in the history.
func TestACutStreamIsSentAgain(t *testing.T) {
	a, p, _ := newAgent(t, mock.Step{Text: "part", Incomplete: true}, mock.Step{Text: "whole"})
	var retries []Retry
	res, err := a.Run(context.Background(), "go", func(e Event) {
		if r, ok := e.(Retry); ok {
			retries = append(retries, r)
		}
	})
	if err != nil || res.Text != "whole" || res.Turns != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(retries) != 1 || retries[0].Attempt != 1 || len(a.History) != 2 || len(p.Requests) != 2 {
		t.Fatalf("retries %v history %d requests %d", retries, len(a.History), len(p.Requests))
	}
	cut := mock.Step{Incomplete: true}
	a, _, _ = newAgent(t, cut, cut, cut, mock.Step{Text: "never"})
	if _, err := a.Run(context.Background(), "go", nil); !errors.Is(err, llm.ErrIncomplete) {
		t.Fatalf("after %d resends: %v", maxCut, err)
	}
}

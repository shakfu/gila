package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/shakfu/gilda/agent"
	"github.com/shakfu/gilda/app"
	"github.com/shakfu/gilda/llm"
	"github.com/shakfu/gilda/tool"
	"github.com/shakfu/gilda/tui"
)

// headless answers one prompt. Text streams to stdout and everything else goes to stderr, so
// stdout is the answer. With asJSON, stdout carries one record per line instead.
func headless(parent context.Context, a *app.App, prompt string, asJSON, color bool) int {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		select {
		case <-interrupt:
			cancel()
		case <-ctx.Done():
		}
	}()

	// A one-shot run asks on the terminal when it has one. --json output is read by a program,
	// so a call that needs approval is refused there.
	if !asJSON {
		a.SetPermissions("", ttyAsk)
	}

	warns, err := a.Prepare(ctx)
	if err != nil {
		if asJSON {
			return writeFailure(os.Stdout, err)
		}
		fmt.Fprintln(os.Stderr, "gilda:", err)
		return 1
	}
	for _, w := range warns {
		if !asJSON {
			fmt.Fprintln(os.Stderr, "gilda: warning:", w)
		}
	}

	diag, st := tui.Writer(os.Stderr, color), tui.NewStyles()
	var emit func(agent.Event)
	var j *jsonOut
	if asJSON {
		j = &jsonOut{w: os.Stdout}
		j.write(map[string]any{"type": "start", "provider": a.ProviderID, "model": a.Agent.Model,
			"session_id": a.Agent.SessionID, "context_window": a.Agent.Context})
		emit = j.event
	} else {
		emit = textEvents(os.Stdout, diag, st)
	}

	res, err := a.Agent.Run(ctx, prompt, func(e agent.Event) {
		if _, ok := e.(agent.Response); ok {
			a.Remember()
		}
		emit(e)
	})
	cancelled := errors.Is(err, context.Canceled) || ctx.Err() != nil

	if asJSON {
		j.result(a, res, err, cancelled)
	} else {
		if res.Turns > 0 {
			fmt.Fprintln(diag, st.Dim.Render(tui.UsageLine(a, res.Usage)))
		}
		if err != nil && !cancelled {
			fmt.Fprintln(diag, st.Error.Render("error: "+err.Error()))
		}
	}
	switch {
	case cancelled:
		return 130
	case err != nil:
		return 1
	}
	return 0
}

func textEvents(out, diag io.Writer, st tui.Styles) func(agent.Event) {
	atLineStart := true
	return func(e agent.Event) {
		switch e := e.(type) {
		case agent.Text:
			fmt.Fprint(out, e.Text)
			if e.Text != "" {
				atLineStart = e.Text[len(e.Text)-1] == '\n'
			}
		case agent.Response:
			if !atLineStart {
				fmt.Fprintln(out)
				atLineStart = true
			}
		case agent.ToolResult:
			fmt.Fprintln(diag, tui.ToolLine(st, e, 0))
		case agent.Retry:
			fmt.Fprintln(diag, st.Warn.Render(tui.RetryLine(e)))
		}
	}
}

type jsonOut struct {
	w io.Writer
}

func (j *jsonOut) write(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(j.w, "%s\n", data)
}

// event prints the records a caller needs to follow a run. Text arrives whole in turn records,
// so its deltas are not printed.
func (j *jsonOut) event(e agent.Event) {
	switch e.(type) {
	case agent.ToolCall, agent.ToolResult, agent.Retry, agent.Response:
		j.write(agent.Record(e))
	}
}

func (j *jsonOut) result(a *app.App, res agent.Result, err error, cancelled bool) {
	outcome, errText := "complete", any(nil)
	switch {
	case cancelled:
		outcome = "cancelled"
	case err != nil:
		outcome, errText = "error", err.Error()
	}
	j.write(map[string]any{
		"type": "result", "outcome": outcome, "text": res.Text, "error": errText,
		"provider": a.ProviderID, "model": a.Agent.Model, "turns": res.Turns, "permissions": a.Mode(),
		"usage": res.Usage, "context_used": a.Agent.Used, "context_window": a.Agent.Context,
	})
}

// writeFailure reports a setup error as the result record, so a caller parsing stdout sees it.
func writeFailure(w io.Writer, err error) int {
	(&jsonOut{w: w}).write(map[string]any{"type": "result", "outcome": "error", "error": err.Error(),
		"text": "", "turns": 0, "usage": llm.Usage{}})
	return 1
}

// ttyAsk asks on the controlling terminal, so the question reaches the user even when stdout
// and stderr are redirected. With no terminal the call is refused.
func ttyAsk(ctx context.Context, _ tool.Tool, _ llm.ToolCall, label string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, errors.New("refused: approval needs a terminal, and there is none")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "gilda: allow %s? [y/N] ", label)
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(tty).ReadString('\n')
		answer <- strings.TrimSpace(strings.ToLower(line))
	}()
	select {
	case a := <-answer:
		return a == "y" || a == "yes", nil
	case <-ctx.Done():
		fmt.Fprintln(tty)
		return false, ctx.Err()
	}
}

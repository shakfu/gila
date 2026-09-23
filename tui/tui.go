// Package tui is gila's REPL. It runs inline, not on the alternate screen: finished output goes
// into the terminal's own scrollback, and only the streaming line, the input and the status bar
// are redrawn.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gila/agent"
	"github.com/shakfu/gila/app"
	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/permission"
	"github.com/shakfu/gila/prompt"
	"github.com/shakfu/gila/state"
)

type Options struct {
	Version string
	Color   bool
}

// Run starts the REPL and returns when the user leaves it.
func Run(ctx context.Context, a *app.App, opts Options) error {
	m := newModel(ctx, a, opts)
	popts := []tea.ProgramOption{tea.WithContext(ctx)}
	if !opts.Color {
		popts = append(popts, tea.WithColorProfile(colorprofile.Ascii))
	}
	_, err := tea.NewProgram(m, popts...).Run()
	if m.cancel != nil {
		m.cancel()
	}
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		err = nil
	}
	if m.prompts > 0 {
		fmt.Fprintln(Writer(os.Stdout, opts.Color), m.st.Dim.Render(m.sessionLine()))
	}
	return err
}

type (
	eventMsg struct{ ev agent.Event }
	doneMsg  struct {
		res agent.Result
		err error
	}
	prepMsg struct{ warns []error }
	// approvalMsg asks the user whether a call may run; the agent waits on reply.
	approvalMsg struct {
		call  llm.ToolCall
		label string
		reply chan bool
	}
	switchedMsg struct{ err error }
	modelsMsg   struct {
		models []llm.Model
		err    error
		// pick opens the picker; otherwise the list is printed, filtered.
		pick   bool
		filter string
	}
)

type model struct {
	ctx  context.Context
	app  *app.App
	opts Options
	st   Styles
	md   markdown

	width int
	input textarea.Model
	spin  spinner.Model
	hist  *state.History
	// histPos indexes hist.Entries while browsing; len(Entries) means the draft.
	histPos int
	draft   string

	// busy names a background step that blocks new prompts, such as "loading".
	busy    string
	running bool
	// stopped is set when the user cancels the run, whatever error the provider returns.
	stopped bool
	cancel  context.CancelFunc
	events  chan tea.Msg
	started time.Time
	queue   []string

	partial   string // assistant text after the last newline
	thinking  string // reasoning after the last newline
	showThink bool
	phase     string // what the status bar says during a run
	lines     []string

	// Display copies, refreshed when no goroutine touches the agent.
	provider, modelID, effort string
	window, used              int64
	session                   llm.Usage
	prompts                   int

	picker *picker
	// approval is the question waiting for y, n or a.
	approval *approvalMsg
	// always holds tools the user allowed for the rest of the session.
	always  map[string]bool
	tabIdx  int
	tabSeed string
}

func newModel(ctx context.Context, a *app.App, opts Options) *model {
	st := NewStyles()
	in := textarea.New()
	in.ShowLineNumbers = false
	in.Prompt = "> "
	in.Placeholder = "Ask gila. Enter sends, Shift-Enter or Ctrl-J adds a line, /help lists commands."
	in.CharLimit = 0
	in.DynamicHeight = true
	in.MinHeight = 1
	in.MaxHeight = 8
	in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	s := in.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Focused.Prompt = st.Accent.Bold(true)
	s.Focused.Placeholder = st.Dim
	in.SetStyles(s)
	in.Focus()

	hist := state.LoadHistory(a.State.Dir())
	m := &model{
		ctx: ctx, app: a, opts: opts, st: st, md: markdown{st: st},
		width: 80, input: in, hist: hist, histPos: len(hist.Entries),
		spin: spinner.New(spinner.WithSpinner(spinner.Line), spinner.WithStyle(st.Warn)),
		busy: "loading", always: map[string]bool{},
	}
	m.sync()
	return m
}

func (m *model) Init() tea.Cmd {
	a := m.app
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		return prepMsg{warns: a.Prepare(m.ctx)}
	})
}

// sync copies what the view shows out of the agent. Call it only while no run or switch is in
// flight, since those mutate the agent from another goroutine.
func (m *model) sync() {
	ag := m.app.Agent
	m.provider, m.modelID, m.effort, m.window = m.app.ProviderID, ag.Model, ag.Effort, ag.Context
}

func (m *model) out(line string) { m.lines = append(m.lines, line) }

// flush prints what this update produced as one block, so its lines stay in order.
func (m *model) flush() tea.Cmd {
	if len(m.lines) == 0 {
		return nil
	}
	text := strings.Join(m.lines, "\n")
	m.lines = nil
	return tea.Println(text)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 20)
		m.input.SetWidth(m.width)
		return m, nil

	case prepMsg:
		m.busy = ""
		m.sync()
		m.banner(msg.warns)
		return m, tea.Sequence(m.flush(), m.next())

	case switchedMsg:
		m.busy = ""
		m.sync()
		if msg.err != nil {
			m.out(m.st.Error.Render("error: " + msg.err.Error()))
		} else {
			m.out(m.st.Dim.Render("using ") + m.st.Model.Render(m.provider+"/"+shortModel(m.modelID)) + m.st.Dim.Render(m.windowNote()))
		}
		return m, tea.Sequence(m.flush(), m.next())

	case modelsMsg:
		m.busy = ""
		return m, tea.Sequence(m.showModels(msg), m.flush(), m.next())

	case eventMsg:
		m.event(msg.ev)
		return m, tea.Sequence(m.flush(), listen(m.events))

	case approvalMsg:
		if m.always[msg.call.Name] {
			msg.reply <- true
		} else {
			m.approval = &msg
			m.phase = "waiting for approval"
		}
		return m, listen(m.events)

	case doneMsg:
		m.finish(msg)
		return m, tea.Sequence(m.flush(), m.next())

	case spinner.TickMsg:
		if !m.running && m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		if m.approval != nil {
			return m, m.approvalKey(msg)
		}
		if m.picker != nil {
			return m, m.pickerKey(msg)
		}
		if cmd, handled := m.key(msg); handled {
			return m, cmd
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// key handles the REPL's own bindings; anything else goes to the input.
func (m *model) key(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if msg.String() != "tab" {
		m.tabSeed = ""
	}
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil, true
		}
		m.input.Reset()
		m.hist.Add(text)
		m.histPos, m.draft = len(m.hist.Entries), ""
		if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") && !strings.Contains(strings.Fields(text)[0][1:], "/") {
			return tea.Sequence(m.command(text), m.flush()), true
		}
		m.queue = append(m.queue, text)
		return tea.Sequence(m.flush(), m.next()), true
	case "esc":
		if m.running {
			m.stopped = true
			m.cancel()
		}
		return nil, true
	case "ctrl+c":
		switch {
		case m.running:
			m.stopped = true
			m.cancel()
		case m.input.Value() != "":
			m.input.Reset()
		default:
			return tea.Quit, true
		}
		return nil, true
	case "ctrl+d":
		if m.input.Value() == "" && !m.running {
			return tea.Quit, true
		}
	case "up", "ctrl+p":
		if !strings.Contains(m.input.Value(), "\n") && m.histPos > 0 {
			if m.histPos == len(m.hist.Entries) {
				m.draft = m.input.Value()
			}
			m.histPos--
			m.input.SetValue(m.hist.Entries[m.histPos])
			return nil, true
		}
	case "down", "ctrl+n":
		if !strings.Contains(m.input.Value(), "\n") && m.histPos < len(m.hist.Entries) {
			m.histPos++
			if m.histPos == len(m.hist.Entries) {
				m.input.SetValue(m.draft)
			} else {
				m.input.SetValue(m.hist.Entries[m.histPos])
			}
			return nil, true
		}
	case "tab":
		return m.complete(), true
	}
	return nil, false
}

// next starts the queued prompt when nothing else is running.
func (m *model) next() tea.Cmd {
	if m.running || m.busy != "" || len(m.queue) == 0 {
		return nil
	}
	text := m.queue[0]
	m.queue = m.queue[1:]
	m.out("")
	for i, l := range strings.Split(text, "\n") {
		p := "  "
		if i == 0 {
			p = "> "
		}
		m.out(m.st.Accent.Bold(true).Render(p) + m.st.User.Render(l))
	}
	return tea.Sequence(m.flush(), m.start(text))
}

func (m *model) start(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel, m.running, m.stopped, m.started, m.phase = cancel, true, false, time.Now(), "waiting"
	m.prompts++
	ch := make(chan tea.Msg, 256)
	m.events = ch
	// Set before the run starts, so the agent goroutine only reads it.
	m.app.SetPermissions("", askVia(ch))
	ag := m.app.Agent
	go func() {
		res, err := ag.Run(ctx, text, func(e agent.Event) { ch <- eventMsg{e} })
		ch <- doneMsg{res, err}
		close(ch)
	}()
	return tea.Batch(listen(ch), m.spin.Tick)
}

// askVia sends approval questions to the REPL through the run's event channel and waits for
// the answer, or for the run to be cancelled.
func askVia(ch chan<- tea.Msg) permission.AskFunc {
	return func(ctx context.Context, call llm.ToolCall, label string) (bool, error) {
		reply := make(chan bool, 1)
		select {
		case ch <- approvalMsg{call: call, label: label, reply: reply}:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		select {
		case ok := <-reply:
			return ok, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (m *model) approvalKey(msg tea.KeyPressMsg) tea.Cmd {
	q := m.approval
	answer := func(ok bool) {
		m.approval, m.phase = nil, q.label
		q.reply <- ok
	}
	switch msg.String() {
	case "y":
		answer(true)
	case "a":
		m.always[q.call.Name] = true
		m.out(m.st.Dim.Render("  " + q.call.Name + " allowed for the rest of the session"))
		answer(true)
	case "n", "esc":
		answer(false)
	case "ctrl+c":
		answer(false)
		m.stopped = true
		m.cancel()
	}
	return m.flush()
}

func listen(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *model) event(ev agent.Event) {
	switch e := ev.(type) {
	case agent.Text:
		m.phase = "writing"
		m.partial += e.Text
		for {
			i := strings.IndexByte(m.partial, '\n')
			if i < 0 {
				break
			}
			m.out(m.md.line(m.partial[:i]))
			m.partial = m.partial[i+1:]
		}
	case agent.Reasoning:
		m.phase = "thinking"
		if !m.showThink {
			return
		}
		m.thinking += e.Text
		for {
			i := strings.IndexByte(m.thinking, '\n')
			if i < 0 {
				break
			}
			if l := m.thinking[:i]; strings.TrimSpace(l) != "" {
				m.out(m.st.Dim.Italic(true).Render("  " + l))
			}
			m.thinking = m.thinking[i+1:]
		}
	case agent.ToolStart:
		m.endText()
		m.phase = "preparing " + e.Name
	case agent.ToolCall:
		m.phase = e.Label
	case agent.ToolResult:
		m.out(ToolLine(m.st, e, m.width))
		m.phase = "waiting"
	case agent.Response:
		m.endText()
		m.app.Remember()
		m.session.Add(e.Usage)
		m.used = e.Usage.Input + e.Usage.Output
	}
}

// endText flushes a partial line and closes the markdown state at the end of a response.
func (m *model) endText() {
	if m.partial != "" {
		m.out(m.md.line(m.partial))
		m.partial = ""
	}
	if m.thinking != "" && m.showThink {
		m.out(m.st.Dim.Italic(true).Render("  " + m.thinking))
	}
	m.thinking = ""
	m.md.reset()
}

func (m *model) finish(d doneMsg) {
	m.endText()
	m.running, m.phase = false, ""
	m.cancel()
	m.sync()
	switch {
	case d.err != nil && (m.stopped || errors.Is(d.err, context.Canceled)):
		m.out(m.st.Warn.Render("cancelled"))
		m.queue = nil
	case agent.ContextFull(d.err):
		m.out(m.st.Error.Render("the conversation no longer fits the context window; /clear starts a new one"))
		m.queue = nil
	case d.err != nil:
		m.out(m.st.Error.Render("error: " + d.err.Error()))
	}
	if d.res.Turns > 0 {
		m.out(m.st.Dim.Render(usageLine(m.used, m.window, d.res.Usage, m.session)))
	}
}

func (m *model) banner(warns []error) {
	m.out(m.st.Banner.Render("gila "+m.opts.Version) + "  " + m.st.Model.Render(m.provider+"/"+shortModel(m.modelID)) +
		m.st.Dim.Render(m.windowNote()+"  "+shortPath(cwd())+"  permissions: "+string(m.app.Mode())))
	for _, p := range prompt.AgentsFiles(cwd(), m.app.ConfigDir()) {
		m.out(m.st.Dim.Render("  instructions: " + shortPath(p)))
	}
	for _, w := range warns {
		m.out(m.st.Warn.Render("warning: " + w.Error()))
	}
}

func (m *model) windowNote() string {
	if m.window > 0 {
		return " (" + shortTokens(m.window) + " context)"
	}
	return ""
}

func (m *model) sessionLine() string {
	u := m.session
	return fmt.Sprintf("session: %d prompts | in %s (%s cached) | out %s | %s",
		m.prompts, shortTokens(u.Input), shortTokens(u.CacheRead), shortTokens(u.Output), money(u.Cost, u.Estimated))
}

func (m *model) View() tea.View {
	var b strings.Builder
	w := m.width
	if m.running && m.showThink && m.thinking != "" {
		b.WriteString(ansi.Hardwrap(m.st.Dim.Italic(true).Render("  "+m.thinking), w, true) + "\n")
	}
	if m.running && m.partial != "" {
		b.WriteString(ansi.Hardwrap(m.partial, w, true) + "\n")
	}
	if m.picker != nil {
		b.WriteString(m.picker.view(m.st, w))
	}
	if q := m.approval; q != nil {
		b.WriteString(m.st.Warn.Bold(true).Render("allow ") + ansi.Truncate(oneLine(q.label), max(w-60, 20), "...") +
			m.st.Warn.Bold(true).Render("?") + m.st.Dim.Render("  y yes | n no | a always for "+q.call.Name+" | Esc no") + "\n")
	}
	b.WriteString(m.st.Dim.Render(strings.Repeat("-", w)) + "\n")
	b.WriteString(m.input.View() + "\n")
	b.WriteString(m.statusBar())
	return tea.NewView(b.String())
}

func (m *model) statusBar() string {
	var left string
	switch {
	case m.running:
		el := time.Since(m.started).Truncate(time.Second)
		left = m.st.BarBusy.Render(m.spin.View()+" "+el.String()) + m.st.BarLeft.Render(m.phase)
	case m.busy != "":
		left = m.st.BarBusy.Render(m.spin.View() + " " + m.busy)
	default:
		left = m.st.BarLeft.Render(shortPath(cwd()))
	}
	if n := len(m.queue); n > 0 {
		left += m.st.BarLeft.Render(fmt.Sprintf("%d queued", n))
	}
	right := m.st.BarModel.Render(m.provider + "/" + shortModel(m.modelID))
	if m.effort != "" {
		right += m.st.BarLeft.Render(m.effort)
	}
	if mode := m.app.Mode(); mode != permission.Auto {
		right += m.st.BarBusy.Render(string(mode))
	}
	ctx := "ctx " + shortTokens(m.used)
	if m.window > 0 {
		ctx = fmt.Sprintf("ctx %d%%", m.used*100/m.window)
	}
	right += m.st.BarCtx.Render(ctx)
	if m.session.Cost != nil {
		right += m.st.BarCost.Render(money(m.session.Cost, m.session.Estimated))
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = ansi.Truncate(left, max(m.width-lipgloss.Width(right)-1, 0), "")
		gap = m.width - lipgloss.Width(left) - lipgloss.Width(right)
	}
	return left + m.st.BarFill.Render(strings.Repeat(" ", max(gap, 0))) + right
}

// shortModel drops the directory from a model id that is a file path, as llama-server reports.
func shortModel(id string) string {
	if strings.HasPrefix(id, "/") {
		return filepath.Base(id)
	}
	return id
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}

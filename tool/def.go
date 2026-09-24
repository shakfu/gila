package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/shakfu/gilda/llm"
)

// Def describes a tool built from functions. New turns it into a Tool that declares its
// effect to the permission modes.
type Def struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the arguments; nil means an object with no properties.
	Schema map[string]any
	// ReadOnly declares that the tool only reads local files: no writes, no processes, no
	// network. See the ReadOnly interface; permission modes then run it without asking.
	ReadOnly bool
	// Paths, when set, names the files a call touches; see the Paths interface. Without it a
	// tool that is not read-only asks before every call in auto mode.
	Paths func(args json.RawMessage) ([]string, error)
	// Hosts, when set, makes this a network tool and names the hosts a call contacts; see
	// the Hosts interface. A network tool cannot be read-only, and should set Paths too,
	// returning no paths when it writes no files.
	Hosts func(args json.RawMessage) ([]string, error)
	// Label names a call for the user, such as "grep TODO"; nil shows the tool's name.
	Label func(args json.RawMessage) string
	Run   func(ctx context.Context, args json.RawMessage) (Result, error)
}

// New builds a tool from d.
func New(d Def) (Tool, error) {
	if err := checkName(d.Name); err != nil {
		return nil, err
	}
	if d.Run == nil {
		return nil, fmt.Errorf("tool %s has no Run", d.Name)
	}
	if d.ReadOnly && d.Hosts != nil {
		return nil, fmt.Errorf("tool %s contacts hosts, so it cannot be read-only", d.Name)
	}
	if d.Schema == nil {
		d.Schema = schema(nil, map[string]any{})
	}
	t := defTool{d}
	// Paths and Hosts are declared by implementing the methods, so a Def gets each method
	// only when it sets the function.
	switch {
	case d.Paths != nil && d.Hosts != nil:
		return pathHostTool{t}, nil
	case d.Paths != nil:
		return pathTool{t}, nil
	case d.Hosts != nil:
		return hostTool{t}, nil
	}
	return t, nil
}

type defTool struct{ d Def }

func (t defTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.d.Name, Description: t.d.Description, Schema: t.d.Schema}
}

func (t defTool) Label(args json.RawMessage) string {
	if t.d.Label == nil {
		return t.d.Name
	}
	return t.d.Label(args)
}

func (t defTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	return t.d.Run(ctx, args)
}

func (t defTool) ReadOnly() bool { return t.d.ReadOnly }

type pathTool struct{ defTool }

func (t pathTool) Paths(args json.RawMessage) ([]string, error) { return t.d.Paths(args) }

type hostTool struct{ defTool }

func (t hostTool) Hosts(args json.RawMessage) ([]string, error) { return t.d.Hosts(args) }

type pathHostTool struct{ defTool }

func (t pathHostTool) Paths(args json.RawMessage) ([]string, error) { return t.d.Paths(args) }
func (t pathHostTool) Hosts(args json.RawMessage) ([]string, error) { return t.d.Hosts(args) }

// validName is what both Anthropic and OpenAI accept as a tool name.
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("tool name %q must be 1-64 letters, digits, _ or -", name)
	}
	return nil
}

// Check reports an invalid or repeated tool name. A repeated name would make one of the
// tools unreachable, since calls are dispatched by name.
func Check(tools []Tool) error {
	seen := map[string]bool{}
	var errs []error
	for _, t := range tools {
		name := t.Spec().Name
		if err := checkName(name); err != nil {
			errs = append(errs, err)
		}
		if seen[name] {
			errs = append(errs, fmt.Errorf("two tools are named %q", name))
		}
		seen[name] = true
	}
	return errors.Join(errs...)
}

// Abs resolves path against the root, as the built-in tools and the permission checks do.
// A custom tool that takes paths should resolve them the same way.
func (e Env) Abs(path string) string { return e.abs(path) }

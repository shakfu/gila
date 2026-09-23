package permission

import (
	"strings"
	"testing"

	"github.com/shakfu/gila/tool"
)

func TestWords(t *testing.T) {
	ok := map[string]string{
		"go test ./...":            "go|test|./...",
		"  git   status  --short ": "git|status|--short",
		`go test -run 'Test A|B'`:  "go|test|-run|Test A|B",
		`grep -r "TODO" .`:         "grep|-r|TODO|.",
		"ls *.go":                  "ls|*.go",
		"echo a=b":                 "echo|a=b",
	}
	for cmd, want := range ok {
		got, fine := words(cmd)
		if !fine || strings.Join(got, "|") != want {
			t.Errorf("words(%q) = %q %v, want %q", cmd, got, fine, want)
		}
	}
	for _, cmd := range []string{
		"go test; rm -rf /", "go test && rm x", "go test || true", "go test | tee x",
		"go test &", "go test > out", "cat < in", "echo $(id)", "echo `id`", "echo $HOME",
		`echo "$HOME"`, `echo "a` + "`id`" + `"`, "echo 'unbalanced", "(go test)", "go test\nrm x",
		`echo a\;b`, "",
	} {
		if _, fine := words(cmd); fine {
			t.Errorf("words(%q) accepted", cmd)
		}
	}
}

func TestCommandAllowlistInAskMode(t *testing.T) {
	root := t.TempDir()
	bash := tool.Bash{Env: tool.Env{Root: root}}
	l := Layers{{Commands: []string{"go test", "git status"}}, {Commands: []string{"ls"}}}
	cases := map[string]verdict{
		"go test ./...":         run,
		"go test":               run,
		"go vet ./...":          askUser,
		"git status --short":    run,
		"git statusx":           askUser,
		"ls -la":                run,
		"go test ./... && rm x": askUser,
		"go test $(rm x)":       askUser,
		"gotest":                askUser,
	}
	for cmd, want := range cases {
		v, _ := l.decide(Ask, root, bash, call("bash", map[string]string{"command": cmd}))
		if v != want {
			t.Errorf("ask %q: got %d, want %d", cmd, v, want)
		}
	}
	// The allowlist loosens ask mode only: read-only still refuses bash.
	if v, _ := l.decide(ReadOnly, root, bash, call("bash", map[string]string{"command": "ls"})); v != refuse {
		t.Error("read-only ran an allowlisted command")
	}
	// A custom tool named bash gets nothing from the allowlist.
	if v, _ := l.decide(Ask, root, custom{"bash"}, call("bash", map[string]string{"command": "ls"})); v != askUser {
		t.Error("a custom tool named bash used the allowlist")
	}
	if err := (Rules{Commands: []string{"go test; rm"}}).Validate(); err == nil {
		t.Error("accepted a chained command entry")
	}
}

func TestExactEntries(t *testing.T) {
	entries := []string{"git status$", "git diff $", "go test"}
	cases := map[string]bool{
		"git status":             true,
		"  git   status ":        true,
		"git status --porcelain": false,
		"git diff":               true,
		"git diff --output=x":    false,
		"go test ./...":          true,
		"git":                    false,
	}
	for cmd, want := range cases {
		if got := allowed(entries, cmd); got != want {
			t.Errorf("allowed(%q) = %v, want %v", cmd, got, want)
		}
	}
	for _, e := range []string{"git status$", "git status $", "ls$"} {
		if err := (Rules{Commands: []string{e}}).Validate(); err != nil {
			t.Errorf("rejected %q: %v", e, err)
		}
	}
	for _, e := range []string{"$", "a | b$", "echo $x$"} {
		if err := (Rules{Commands: []string{e}}).Validate(); err == nil {
			t.Errorf("accepted %q", e)
		}
	}
}

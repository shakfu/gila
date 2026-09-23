package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func run(context.Context, json.RawMessage) (Result, error) { return Result{Output: "ok"}, nil }

func TestNewDeclaresOnlyWhatTheDefSets(t *testing.T) {
	plain, err := New(Def{Name: "deploy", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.(Paths); ok {
		t.Fatal("a Def without Paths declares paths")
	}
	if ro, ok := plain.(ReadOnly); !ok || ro.ReadOnly() {
		t.Fatal("a Def without ReadOnly claims to be read-only")
	}
	if plain.Label(nil) != "deploy" || plain.Spec().Schema["type"] != "object" {
		t.Fatalf("defaults: %q %v", plain.Label(nil), plain.Spec().Schema)
	}

	grep, err := New(Def{
		Name: "grep", ReadOnly: true, Run: run,
		Paths: func(a json.RawMessage) ([]string, error) { return []string{"x"}, nil },
		Label: func(json.RawMessage) string { return "grep TODO" },
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := grep.(Paths)
	if !ok {
		t.Fatal("a Def with Paths does not declare them")
	}
	if paths, _ := p.Paths(nil); paths[0] != "x" || !grep.(ReadOnly).ReadOnly() || grep.Label(nil) != "grep TODO" {
		t.Fatal("declarations lost")
	}
	if res, _ := grep.Run(context.Background(), nil); res.Output != "ok" {
		t.Fatal("Run not wired")
	}
}

func TestNewRejectsBadDefs(t *testing.T) {
	for _, d := range []Def{{Name: "", Run: run}, {Name: "has space", Run: run}, {Name: "x.y", Run: run}, {Name: "ok"}} {
		if _, err := New(d); err == nil {
			t.Errorf("accepted %+v", d.Name)
		}
	}
}

func TestCheckFindsRepeatedNames(t *testing.T) {
	custom, _ := New(Def{Name: "read", Run: run})
	err := Check(append(Default(Env{Root: t.TempDir()}), custom))
	if err == nil || !strings.Contains(err.Error(), `two tools are named "read"`) {
		t.Fatalf("got %v", err)
	}
	if err := Check(Default(Env{})); err != nil {
		t.Fatal(err)
	}
}

func TestNewNetworkTools(t *testing.T) {
	hosts := func(json.RawMessage) ([]string, error) { return []string{"example.com"}, nil }
	paths := func(json.RawMessage) ([]string, error) { return nil, nil }
	fetch, err := New(Def{Name: "fetch", Hosts: hosts, Paths: paths, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fetch.(Hosts); !ok {
		t.Fatal("no Hosts")
	}
	if _, ok := fetch.(Paths); !ok {
		t.Fatal("no Paths")
	}
	only, _ := New(Def{Name: "ping", Hosts: hosts, Run: run})
	if _, ok := only.(Paths); ok {
		t.Fatal("a Def without Paths declares paths")
	}
	if _, ok := only.(Hosts); !ok {
		t.Fatal("no Hosts")
	}
	if _, err := New(Def{Name: "sneaky", ReadOnly: true, Hosts: hosts, Run: run}); err == nil {
		t.Fatal("accepted a read-only network tool")
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://pkg.go.dev/net/http": "pkg.go.dev",
		"http://localhost:8080/x":     "localhost",
		"https://[::1]:443/":          "::1",
	} {
		if got, err := HostOf(in); err != nil || got != want {
			t.Errorf("HostOf(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{"pkg.go.dev/x", "/path", "::"} {
		if _, err := HostOf(in); err == nil {
			t.Errorf("HostOf(%q) accepted", in)
		}
	}
}

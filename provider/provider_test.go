package provider

import "testing"

func TestCompatNeedsAnEndpointAndTakesAnOptionalKey(t *testing.T) {
	t.Setenv("COMPAT_BASE_URL", "")
	t.Setenv("COMPAT_API_KEY", "")
	if _, err := Open("compat", "", ""); err == nil {
		t.Fatal("compat opened without an endpoint")
	}
	if p, err := Open("compat", "", "http://box:1234/v1"); err != nil || p.Name() != "compat" {
		t.Fatalf("%v", err)
	}
	t.Setenv("COMPAT_BASE_URL", "http://box:1234/v1")
	if _, err := Open("compat", "", ""); err != nil {
		t.Fatalf("env endpoint: %v", err)
	}
}

// A local server is never chosen by itself, even with its optional key set.
func TestChooseSkipsLocalServers(t *testing.T) {
	for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(v, "")
	}
	t.Setenv("COMPAT_API_KEY", "k")
	if id, err := Choose("", nil); err == nil {
		t.Fatalf("chose %s", id)
	}
	if id, err := Choose("compat", nil); err != nil || id != "compat" {
		t.Fatalf("remembered compat: %s %v", id, err)
	}
}

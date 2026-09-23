package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gila-test")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "gila")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// gila runs the binary in a scratch directory with its own state, and returns stdout, stderr
// and the exit status.
func gila(t *testing.T, script string, args ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "mock.json"), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append([]string{"--mock", "mock.json"}, args...)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+dir, "XDG_CACHE_HOME="+dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

const script = `[
  {"text": "Listing.", "calls": [{"name": "bash", "arguments": {"command": "echo hi"}}], "usage": {"input_tokens": 10, "output_tokens": 2, "cost": 0.001}},
  {"text": "The answer.", "usage": {"input_tokens": 20, "output_tokens": 3, "cost": 0.002}}
]`

func TestHeadlessPrintsOnlyTheAnswerOnStdout(t *testing.T) {
	stdout, stderr, code := gila(t, script, "-p", "go")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if stdout != "Listing.\nThe answer.\n" {
		t.Fatalf("stdout %q", stdout)
	}
	if !strings.Contains(stderr, "$ echo hi -> hi") || !strings.Contains(stderr, "$0.0030") {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestJSONEndsInAResultRecord(t *testing.T) {
	stdout, _, code := gila(t, script, "-p", "go", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var types []string
	var last map[string]any
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("not JSON: %q", sc.Text())
		}
		types = append(types, rec["type"].(string))
		last = rec
	}
	want := "start turn tool_call tool_result turn result"
	if strings.Join(types, " ") != want {
		t.Fatalf("records %v", types)
	}
	usage := last["usage"].(map[string]any)
	if last["outcome"] != "complete" || last["text"] != "The answer." || usage["cost"] != 0.003 || last["turns"] != float64(2) {
		t.Fatalf("result %v", last)
	}
}

func TestErrorsExitOneAndAppearInTheResult(t *testing.T) {
	stdout, _, code := gila(t, `[{"error": "upstream exploded"}]`, "-p", "go", "--json")
	if code != 1 || !strings.Contains(stdout, `"outcome":"error"`) || !strings.Contains(stdout, "upstream exploded") {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"stray"}, {"-p", "x", "--effort", "extreme"}} {
		if _, stderr, code := gila(t, "", args...); code != 2 || !strings.HasPrefix(stderr, "gila:") {
			t.Errorf("%v: exit %d stderr %q", args, code, stderr)
		}
	}
}

func TestPromptFromStdin(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "m.json"), []byte(`[{"text":"ok"}]`), 0o644)
	cmd := exec.Command(bin, "--mock", "m.json", "-p", "-", "--json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+dir)
	cmd.Stdin = strings.NewReader("from stdin")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), `"outcome":"complete"`) {
		t.Fatalf("%v %s", err, out)
	}
}

// An explicit -p is headless even when stdin turns out empty.
func TestEmptyPromptFromStdinIsAnError(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(bin, "--mock", "m.json", "-p", "-", "--json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+dir)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(out), `"outcome":"error"`) {
		t.Fatalf("%v %s", err, out)
	}
}

// --json has no one to ask, so auto refuses a write outside the root, and read-only refuses
// every write; the model gets the reason and the run completes.
func TestPermissionsInJSONMode(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "escape.txt")
	script := `[{"calls":[{"name":"write","arguments":{"path":"` + outside + `","content":"x"}},
	                      {"name":"write","arguments":{"path":"inside.txt","content":"x"}}]},
	            {"text":"ok"}]`
	for mode, wantInside := range map[string]bool{"auto": true, "read-only": false} {
		stdout, _, code := gila(t, script, "-p", "go", "--json", "--permissions", mode)
		if code != 0 {
			t.Fatalf("%s: exit %d", mode, code)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Fatalf("%s: wrote outside the root", mode)
		}
		refusals := strings.Count(stdout, `"error":"refused:`)
		if want := map[bool]int{true: 1, false: 2}[wantInside]; refusals != want {
			t.Fatalf("%s: %d refusals, want %d\n%s", mode, refusals, want, stdout)
		}
		if !strings.Contains(stdout, `"permissions":"`+mode+`"`) {
			t.Fatalf("%s: result lacks the mode", mode)
		}
	}
	if _, stderr, code := gila(t, "", "-p", "x", "--permissions", "yolo"); code != 2 || !strings.Contains(stderr, "permissions") {
		t.Fatalf("bad mode: %d %q", code, stderr)
	}
}

func TestHelpFitsEightyColumns(t *testing.T) {
	stdout, _, code := gila(t, "", "--help")
	if code != 0 || !strings.Contains(stdout, "--permissions MODE") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if len(line) > 80 {
			t.Errorf("%d columns: %q", len(line), line)
		}
	}
}

// settings.toml adds protections; it cannot lift a built-in one, and a bad file stops the run.
func TestSettingsAddProtections(t *testing.T) {
	script := `[{"calls":[{"name":"write","arguments":{"path":".git/x","content":"1"}},
	                      {"name":"write","arguments":{"path":"key.pem","content":"2"}},
	                      {"name":"write","arguments":{"path":"ok.txt","content":"3"}}]},
	            {"text":"ok"}]`
	settings := "[permissions]\nsecrets = [\"*.pem\"]\nprotected = [\"!.git\"]\n"
	stdout, _ := gilaWithSettings(t, settings, script, 0, "-p", "go", "--json")
	for _, want := range []string{
		"refused: auto mode asks before write under .git",
		"refused: auto mode asks before write under key.pem",
		`"label":"write ok.txt","name":"write","ok":true`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	_, stderr := gilaWithSettings(t, "[permissions]\nsecret = []\n", script, 1, "-p", "go")
	if !strings.Contains(stderr, "unknown keys: permissions.secret") {
		t.Errorf("bad settings: %q", stderr)
	}
}

func gilaWithSettings(t *testing.T, settings, script string, wantCode int, args ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cfg", "gila"), 0o700)
	os.WriteFile(filepath.Join(dir, "cfg", "gila", "settings.toml"), []byte(settings), 0o600)
	os.WriteFile(filepath.Join(dir, "mock.json"), []byte(script), 0o600)
	cmd := exec.Command(bin, append([]string{"--mock", "mock.json"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "cfg"))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	if code != wantCode {
		t.Fatalf("exit %d, want %d: %s", code, wantCode, errb.String())
	}
	return out.String(), errb.String()
}

// The settings file's mode applies unless --permissions names one.
func TestSettingsMode(t *testing.T) {
	script := `[{"calls":[{"name":"write","arguments":{"path":"ok.txt","content":"1"}}]},{"text":"ok"}]`
	settings := "[permissions]\nmode = \"read-only\"\n"
	stdout, _ := gilaWithSettings(t, settings, script, 0, "-p", "go", "--json")
	if !strings.Contains(stdout, `"permissions":"read-only"`) || !strings.Contains(stdout, "refused: write") {
		t.Fatalf("settings mode not applied:\n%s", stdout)
	}
	stdout, _ = gilaWithSettings(t, settings, script, 0, "-p", "go", "--json", "--permissions", "auto")
	if !strings.Contains(stdout, `"permissions":"auto"`) || strings.Contains(stdout, "refused") {
		t.Fatalf("flag did not win:\n%s", stdout)
	}
}

// In ask mode an allowlisted command runs; any other, or a chained one, is refused when no one
// can be asked.
func TestCommandAllowlist(t *testing.T) {
	script := `[{"calls":[{"name":"bash","arguments":{"command":"echo hi"}},
	                      {"name":"bash","arguments":{"command":"echo hi; touch x"}},
	                      {"name":"bash","arguments":{"command":"touch y"}}]},
	            {"text":"ok"}]`
	settings := "[permissions]\nmode = \"ask\"\ncommands = [\"echo\"]\n"
	stdout, _ := gilaWithSettings(t, settings, script, 0, "-p", "go", "--json")
	if !strings.Contains(stdout, `"label":"$ echo hi","name":"bash","ok":true`) {
		t.Errorf("allowlisted command did not run:\n%s", stdout)
	}
	if strings.Count(stdout, "refused: ask mode asks before bash") != 2 {
		t.Errorf("want 2 refusals:\n%s", stdout)
	}
}

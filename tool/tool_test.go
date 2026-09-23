package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func env(t *testing.T) Env {
	t.Helper()
	return Env{Root: t.TempDir(), Jobs: &Jobs{}}
}

func args(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadNumbersLinesAndReportsTheRest(t *testing.T) {
	e := env(t)
	write(t, filepath.Join(e.Root, "f.txt"), "a\nb\r\nc\nd\n")
	res, err := Read{e}.Run(context.Background(), args(t, map[string]any{"path": "f.txt", "offset": 2, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	want := "     2\tb\n     3\tc\n... 1 line not shown; continue with offset 4\n"
	if res.Output != want {
		t.Fatalf("got %q, want %q", res.Output, want)
	}
	if res.Summary != "2 lines" {
		t.Fatalf("summary %q", res.Summary)
	}
}

func TestReadPastTheEndSaysHowLongTheFileIs(t *testing.T) {
	e := env(t)
	write(t, filepath.Join(e.Root, "f.txt"), "a\nb\n")
	res, err := Read{e}.Run(context.Background(), args(t, map[string]any{"path": "f.txt", "offset": 9}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "has 2 lines; none at offset 9") {
		t.Fatalf("got %q", res.Output)
	}
}

func TestReadRefusesDirectoriesAndSummarisesBinaries(t *testing.T) {
	e := env(t)
	if _, err := (Read{e}).Run(context.Background(), args(t, map[string]any{"path": "."})); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory: %v", err)
	}
	write(t, filepath.Join(e.Root, "b.bin"), "ab\x00cd")
	res, err := Read{e}.Run(context.Background(), args(t, map[string]any{"path": "b.bin"}))
	if err != nil || res.Output != "b.bin is binary, 5 bytes" {
		t.Fatalf("binary: %q %v", res.Output, err)
	}
}

func TestReadCutsLongLines(t *testing.T) {
	e := env(t)
	write(t, filepath.Join(e.Root, "long.txt"), strings.Repeat("x", 3*lineCap)+"\nshort\n")
	res, err := Read{e}.Run(context.Background(), args(t, map[string]any{"path": "long.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(res.Output, "\n")
	if got := len(strings.TrimPrefix(lines[0], "     1\t")); got != lineCap {
		t.Fatalf("first line kept %d bytes, want %d", got, lineCap)
	}
	if lines[1] != "     2\tshort" || !strings.Contains(res.Output, "long lines were cut") {
		t.Fatalf("got %q", res.Output)
	}
}

func TestWriteCreatesParentsAndKeepsModeAndSymlinks(t *testing.T) {
	e := env(t)
	res, err := Write{e}.Run(context.Background(), args(t, map[string]any{"path": "a/b/c.txt", "content": "hi"}))
	if err != nil || res.Summary != "2 bytes" {
		t.Fatalf("%v %q", err, res.Summary)
	}
	if data, _ := os.ReadFile(filepath.Join(e.Root, "a/b/c.txt")); string(data) != "hi" {
		t.Fatalf("content %q", data)
	}

	target := filepath.Join(e.Root, "script.sh")
	write(t, target, "old")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.Root, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Write{e}).Run(context.Background(), args(t, map[string]any{"path": "link.sh", "content": "new"})); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a file")
	}
	st, _ := os.Stat(target)
	if data, _ := os.ReadFile(target); string(data) != "new" || st.Mode().Perm() != 0o755 {
		t.Fatalf("target %q mode %v", data, st.Mode())
	}
	entries, _ := os.ReadDir(e.Root)
	for _, en := range entries {
		if strings.Contains(en.Name(), ".gila-") {
			t.Fatalf("temporary file left behind: %s", en.Name())
		}
	}
}

func TestEditRequiresAUniqueMatch(t *testing.T) {
	e := env(t)
	path := filepath.Join(e.Root, "f.go")
	write(t, path, "x := 1\nx := 1\n")
	run := func(a map[string]any) (Result, error) {
		a["path"] = "f.go"
		return Edit{e}.Run(context.Background(), args(t, a))
	}
	if _, err := run(map[string]any{"old_string": "x := 1", "new_string": "y"}); err == nil ||
		!strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := run(map[string]any{"old_string": "zzz", "new_string": "y"}); err == nil {
		t.Fatal("missing text was accepted")
	}
	if _, err := run(map[string]any{"old_string": "", "new_string": "y"}); err == nil {
		t.Fatal("empty old_string was accepted")
	}
	res, err := run(map[string]any{"old_string": "x := 1", "new_string": "y := 2", "replace_all": true})
	if err != nil || res.Summary != "2 replacements" {
		t.Fatalf("%v %q", err, res.Summary)
	}
	if data, _ := os.ReadFile(path); string(data) != "y := 2\ny := 2\n" {
		t.Fatalf("content %q", data)
	}
}

// read shows lines without their \r, so a multi-line old_string copied from it must still match
// a CRLF file, and the replacement must keep the file's line endings.
func TestEditMatchesCRLFFiles(t *testing.T) {
	e := env(t)
	path := filepath.Join(e.Root, "win.txt")
	write(t, path, "one\r\ntwo\r\nthree\r\n")
	_, err := Edit{e}.Run(context.Background(), args(t, map[string]any{
		"path": "win.txt", "old_string": "one\ntwo", "new_string": "1\n2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "1\r\n2\r\nthree\r\n" {
		t.Fatalf("content %q", data)
	}
}

func TestBashReportsExitStatusAndOutput(t *testing.T) {
	e := env(t)
	res, err := Bash{e}.Run(context.Background(), args(t, map[string]any{"command": "echo out; echo err >&2; exit 3"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || !strings.Contains(res.Output, "out\nerr\n") || !strings.Contains(res.Output, "[exit 3]") {
		t.Fatalf("got %+v", res)
	}
	if res.Summary != "exit 3: err" {
		t.Fatalf("summary %q", res.Summary)
	}
}

func TestBashRunsInTheRoot(t *testing.T) {
	e := env(t)
	res, err := Bash{e}.Run(context.Background(), args(t, map[string]any{"command": "pwd -P"}))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(e.Root)
	if strings.TrimSpace(res.Output) != root {
		t.Fatalf("pwd %q, want %q", res.Output, root)
	}
}

func TestBashRefusesALoginShellWrapper(t *testing.T) {
	for _, cmd := range []string{"bash -lc 'ls'", "/bin/zsh -l -c ls", "sh --login -c ls"} {
		_, err := Bash{env(t)}.Run(context.Background(), args(t, map[string]any{"command": cmd}))
		if err == nil || !strings.Contains(err.Error(), "login shell") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
	if _, err := (Bash{env(t)}).Run(context.Background(), args(t, map[string]any{"command": "bash -c 'true'"})); err != nil {
		t.Errorf("plain bash -c was refused: %v", err)
	}
}

func TestBashTimeoutKillsTheProcessGroup(t *testing.T) {
	e := env(t)
	start := time.Now()
	res, err := Bash{e}.Run(context.Background(), args(t, map[string]any{"command": "sleep 30 | cat", "timeout": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the pipeline outlived its timeout")
	}
	if !res.Failed || !strings.Contains(res.Output, "killed after 1 s timeout") {
		t.Fatalf("got %+v", res)
	}
}

func TestBashCancelStopsTheCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := Bash{env(t)}.Run(ctx, args(t, map[string]any{"command": "sleep 30"}))
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

// A background job keeps the pipe open. The call must still return, say so, and the job must
// die when the agent's jobs are killed.
func TestBashBackgroundJobsAreReportedAndKilled(t *testing.T) {
	e := env(t)
	marker := filepath.Join(e.Root, "alive")
	cmd := "(while true; do touch " + marker + "; sleep 0.1; done) & echo started"
	res, err := Bash{e}.Run(context.Background(), args(t, map[string]any{"command": cmd}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "background processes still running") {
		t.Fatalf("got %q", res.Output)
	}
	e.Jobs.Kill()
	time.Sleep(300 * time.Millisecond)
	os.Remove(marker)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("background job survived Kill")
	}
}

func TestCaptureKeepsHeadAndTail(t *testing.T) {
	c := &capture{limit: 100}
	for i := range 1000 {
		c.Write([]byte{byte('a' + i%26)})
	}
	out := c.String()
	if !strings.HasPrefix(out, "abcdefghijklmnopqrst") || !strings.Contains(out, "bytes omitted") {
		t.Fatalf("got %q", out)
	}
	if !strings.HasSuffix(out, "ghijkl") {
		t.Fatalf("tail lost: %q", out)
	}
}

func TestCapKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("é", 100) + "\xff"
	out := Cap(s, 50)
	if !utf8.ValidString(out) || !strings.Contains(out, "omitted") {
		t.Fatalf("got %q", out)
	}
	if Cap("short", 50) != "short" {
		t.Fatal("short text changed")
	}
}

func TestInvalidArgumentsAreAnError(t *testing.T) {
	_, err := Read{env(t)}.Run(context.Background(), json.RawMessage(`{"path": 3}`))
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("got %v", err)
	}
}

func TestLabels(t *testing.T) {
	e := env(t)
	cases := map[string]string{
		Read{e}.Label(args(t, map[string]any{"path": "a.go"})):                           "read a.go",
		Read{e}.Label(args(t, map[string]any{"path": "a.go", "offset": 10, "limit": 5})): "read a.go:10-14",
		Bash{e}.Label(args(t, map[string]any{"command": "go test"})):                     "$ go test",
		Edit{e}.Label(args(t, map[string]any{"path": "x"})):                              "edit x",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// bash output is capped once, so the marker reports what was really dropped.
func TestBashLargeOutputIsCappedOnce(t *testing.T) {
	res, err := Bash{env(t)}.Run(context.Background(), args(t, map[string]any{"command": "seq 1 100000"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) > OutputCap {
		t.Fatalf("output %d bytes, over the cap", len(res.Output))
	}
	if Cap(res.Output, OutputCap) != res.Output {
		t.Fatal("the agent's cap would cut the output again")
	}
	if !strings.Contains(res.Output, "\n100000\n") && !strings.HasSuffix(strings.TrimSpace(strings.Split(res.Output, "[")[0]), "100000") {
		t.Fatalf("tail lost: %q", res.Output[len(res.Output)-100:])
	}
}

func TestBashSummaryDropsControlCharacters(t *testing.T) {
	res, err := Bash{env(t)}.Run(context.Background(), args(t, map[string]any{"command": `printf '10%%\r50%%\r\033[32mdone\033[0m\n'`}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "done" {
		t.Fatalf("summary %q", res.Summary)
	}
}

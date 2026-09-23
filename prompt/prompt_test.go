package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mkfile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The user's file comes first, then the repository's from the root down, so the nearest file
// comes last. A directory above the repository root is not read.
func TestAgentsFilesRunFromUserToNearest(t *testing.T) {
	base := t.TempDir()
	cfg := filepath.Join(base, "config")
	repo := filepath.Join(base, "outer", "repo")
	sub := filepath.Join(repo, "pkg", "x")
	mkfile(t, filepath.Join(cfg, AgentsFile), "user")
	mkfile(t, filepath.Join(base, "outer", AgentsFile), "above the repo")
	mkfile(t, filepath.Join(repo, ".git", "HEAD"), "ref")
	mkfile(t, filepath.Join(repo, AgentsFile), "root")
	mkfile(t, filepath.Join(sub, AgentsFile), "nearest")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := AgentsFiles(sub, cfg)
	want := []string{filepath.Join(cfg, AgentsFile), filepath.Join(repo, AgentsFile), filepath.Join(sub, AgentsFile)}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", got, want)
	}

	p := Build(sub, cfg, Options{})
	if !strings.HasPrefix(p, "You are gila") || !strings.Contains(p, "- Working directory: "+sub) ||
		!strings.Contains(p, runtime.GOOS) || !strings.Contains(p, "- Command shell: bash") {
		t.Fatalf("environment missing:\n%s", p)
	}
	if strings.Index(p, "user") > strings.Index(p, "root") || strings.Index(p, "root") > strings.Index(p, "nearest") {
		t.Fatalf("order wrong:\n%s", p)
	}
	if strings.Contains(p, "above the repo") {
		t.Fatal("read a file above the repository root")
	}
}

func TestWithoutARepositoryOnlyTheDirectorysFileApplies(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "work")
	mkfile(t, filepath.Join(base, AgentsFile), "parent")
	mkfile(t, filepath.Join(dir, AgentsFile), "here")
	got := AgentsFiles(dir, "")
	if len(got) != 1 || got[0] != filepath.Join(dir, AgentsFile) {
		t.Fatalf("got %v", got)
	}
}

func TestBlankInstructionsAreSkipped(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, AgentsFile), " \n\n")
	if strings.Contains(Build(dir, "", Options{}), "# "+filepath.Join(dir, AgentsFile)) {
		t.Fatal("a blank file got a section")
	}
}

func TestSkillsNeedFrontmatterWithADescription(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "zip", "SKILL.md"), "---\nname: zip\ndescription: Pack files.\n---\nbody")
	mkfile(t, filepath.Join(dir, "audit", "SKILL.md"), "---\r\ndescription: Check deps.\r\n---\r\n")
	mkfile(t, filepath.Join(dir, "bare", "SKILL.md"), "no frontmatter")
	mkfile(t, filepath.Join(dir, "nameless", "SKILL.md"), "---\nname: x\n---\n")
	mkfile(t, filepath.Join(dir, "huge", "SKILL.md"), "---\ndescription: "+strings.Repeat("x", maxFrontmatter)+"\n---\n")
	mkfile(t, filepath.Join(dir, "notes.txt"), "not a skill")

	skills := Skills(dir)
	if len(skills) != 2 || !strings.HasSuffix(skills[0].Path, "audit/SKILL.md") || skills[0].Frontmatter != "description: Check deps." {
		t.Fatalf("got %+v", skills)
	}
	if skills[1].Frontmatter != "name: zip\ndescription: Pack files." {
		t.Fatalf("got %q", skills[1].Frontmatter)
	}
}

// AGENTS.md and skills are sent with every request, so each can be left out.
func TestAgentsAndSkillsCanBeLeftOut(t *testing.T) {
	dir, cfg := t.TempDir(), t.TempDir()
	mkfile(t, filepath.Join(dir, AgentsFile), "house rules")
	mkfile(t, filepath.Join(cfg, "skills", "zip", "SKILL.md"), "---\ndescription: Pack files.\n---\n")
	for o, want := range map[Options][2]bool{
		{}:               {true, true},
		{NoAgents: true}: {false, true},
		{NoSkills: true}: {true, false},
	} {
		p := Build(dir, cfg, o)
		if strings.Contains(p, "house rules") != want[0] || strings.Contains(p, "Pack files.") != want[1] {
			t.Errorf("%+v:\n%s", o, p)
		}
	}
}

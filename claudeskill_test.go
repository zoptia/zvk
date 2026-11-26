package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeTestEnv isolates ~/.claude and the zvk root in temporary directories.
func claudeTestEnv(t *testing.T) (home, root string) {
	t.Helper()
	home, root = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ZVK_NO_DOCS", "")
	t.Setenv("ZVK_NO_CLAUDE_MD", "")
	return home, root
}

func readText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillsReplaceLegacyImports(t *testing.T) {
	home, root := claudeTestEnv(t)
	for _, f := range []string{"zig", "fetch", "serve"} {
		putFile(t, filepath.Join(root, f, "CLAUDE.md"), "# "+f+"\n")
	}
	global := filepath.Join(home, ".claude", "CLAUDE.md")
	putFile(t, global, "# Mine\nkeep me\n\n# Added by zvk\n@"+filepath.Join(root, "fetch", "CLAUDE.md")+
		"\n# Added by zvk\n@"+filepath.Join(root, "CLAUDE.md")+"\n")

	installClaudeSkills(root, io.Discard)

	for _, f := range []string{"fetch", "serve"} {
		if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
			t.Errorf("legacy %s doc dir not removed: %v", f, err)
		}
	}
	agg := readText(t, filepath.Join(root, "CLAUDE.md"))
	if !strings.Contains(agg, filepath.Join(root, "zig", "CLAUDE.md")) || strings.Contains(agg, "fetch") || strings.Contains(agg, "serve") {
		t.Fatalf("aggregate should import only zig:\n%s", agg)
	}
	want := "# Mine\nkeep me\n\n# Added by zvk\n@" + filepath.Join(root, "CLAUDE.md") + "\n"
	if got := readText(t, global); got != want {
		t.Fatalf("global CLAUDE.md:\n%q\nwant\n%q", got, want)
	}
	for _, s := range zvkSkills() {
		got := readText(t, filepath.Join(home, ".claude", "skills", s.name, "SKILL.md"))
		if !strings.HasPrefix(got, "---\nname: "+s.name+"\ndescription: ") || !strings.Contains(got, zvkSkillMarker) {
			t.Fatalf("bad skill %s:\n%s", s.name, got)
		}
		if strings.Contains(s.description, "\n") || strings.Contains(s.description, ": ") {
			t.Errorf("skill %s description must be a single plain YAML line", s.name)
		}
	}

	// A second run is a no-op for the global file.
	installClaudeSkills(root, io.Discard)
	if got := readText(t, global); got != want {
		t.Fatalf("re-run changed global CLAUDE.md:\n%q", got)
	}
}

func TestNoFeatureDocsRemovesAggregateImport(t *testing.T) {
	home, root := claudeTestEnv(t)
	putFile(t, filepath.Join(root, "fetch", "CLAUDE.md"), "# fetch\n")
	putFile(t, filepath.Join(root, "CLAUDE.md"), "@"+filepath.Join(root, "fetch", "CLAUDE.md")+"\n")
	global := filepath.Join(home, ".claude", "CLAUDE.md")
	putFile(t, global, "# Mine\n\n# Added by zvk\n@"+filepath.Join(root, "CLAUDE.md")+"\n")

	installClaudeSkills(root, io.Discard)

	if fileExists(filepath.Join(root, "CLAUDE.md")) {
		t.Fatal("aggregate should be removed when no feature doc exists")
	}
	if got := readText(t, global); got != "# Mine\n" {
		t.Fatalf("global CLAUDE.md: %q", got)
	}
}

func TestNoGlobalFileCreatedJustToRemove(t *testing.T) {
	home, root := claudeTestEnv(t)
	installClaudeSkills(root, io.Discard)
	if fileExists(filepath.Join(home, ".claude", "CLAUDE.md")) {
		t.Fatal("global CLAUDE.md created with nothing to import")
	}
}

func TestUserSkillNotOverwritten(t *testing.T) {
	home, root := claudeTestEnv(t)
	mine := filepath.Join(home, ".claude", "skills", "zvk-fetch", "SKILL.md")
	putFile(t, mine, "my own skill\n")

	installClaudeSkills(root, io.Discard)

	if got := readText(t, mine); got != "my own skill\n" {
		t.Fatalf("user skill overwritten: %q", got)
	}
	if !fileExists(filepath.Join(home, ".claude", "skills", "zvk-serve", "SKILL.md")) {
		t.Fatal("other skills should still be written")
	}
}

func TestNoClaudeMdSkipsSkills(t *testing.T) {
	home, root := claudeTestEnv(t)
	t.Setenv("ZVK_NO_CLAUDE_MD", "1")
	installClaudeSkills(root, io.Discard)
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude touched despite ZVK_NO_CLAUDE_MD: %v", err)
	}
}

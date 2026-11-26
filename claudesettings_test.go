package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

const surveyKey = "CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY"

func TestClaudeEnvDefaultPreservesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	putFile(t, path, `{
  "theme": "dark",
  "hooks": {"Stop": [{"command": "a <b> && c ñ é"}]},
  "env": {"FOO": "bar"},
  "empty": []
}`)
	changed, err := setClaudeEnvDefault(path, surveyKey, "1")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := `{
  "theme": "dark",
  "hooks": {
    "Stop": [
      {
        "command": "a <b> && c ñ é"
      }
    ]
  },
  "env": {
    "FOO": "bar",
    "CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY": "1"
  },
  "empty": []
}
`
	if got := readText(t, path); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v", info.Mode())
	}
}

func TestClaudeEnvDefaultRespectsExistingValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"env": {"` + surveyKey + `": "0"}}`
	putFile(t, path, orig)
	if changed, err := setClaudeEnvDefault(path, surveyKey, "1"); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if readText(t, path) != orig {
		t.Fatal("file rewritten although the key was already set")
	}
}

func TestClaudeEnvDefaultCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	if _, err := setClaudeEnvDefault(path, surveyKey, "1"); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"env\": {\n    \"" + surveyKey + "\": \"1\"\n  }\n}\n"
	if got := readText(t, path); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestClaudeEnvDefaultLeavesInvalidFiles(t *testing.T) {
	for _, orig := range []string{`{"theme": `, `[]`, `{"env": "x"}`, `{} {}`, `// comment
{}`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		putFile(t, path, orig)
		if _, err := setClaudeEnvDefault(path, surveyKey, "1"); err == nil {
			t.Errorf("%q: expected an error", orig)
		}
		if readText(t, path) != orig {
			t.Errorf("%q: file modified", orig)
		}
	}
}

func TestClaudeEnvDefaultWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	putFile(t, target, `{}`)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := setClaudeEnvDefault(link, surveyKey, "1"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a regular file")
	}
	if readText(t, target) == "{}" {
		t.Fatal("target not updated")
	}
}

func TestConfigureClaudeCodeHonoursNoClaudeMd(t *testing.T) {
	home, _ := claudeTestEnv(t)
	t.Setenv("ZVK_NO_CLAUDE_MD", "1")
	configureClaudeCode(io.Discard)
	if fileExists(filepath.Join(home, ".claude", "settings.json")) {
		t.Fatal("settings.json written despite ZVK_NO_CLAUDE_MD")
	}
}

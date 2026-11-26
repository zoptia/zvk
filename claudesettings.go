package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// ============================================================================
// Claude Code defaults.
//
// zvk turns off Claude Code's in-session "How is Claude doing?" survey by
// adding CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY=1 to the `env` block of the user's
// ~/.claude/settings.json. It runs after `zvk app install claude-code` (whether
// or not Claude Code was already installed) and on `zvk self-install` when
// Claude Code is present.
//
// The edit is minimal: only `env` gains a key; every other member keeps its
// order and value. A key the user already set (to any value) is respected, a
// settings file that is not a JSON object is left untouched, and a symlinked
// settings file is written through to its target. Gated by ZVK_NO_CLAUDE_MD
// (no writes into ~/.claude). Best-effort: failures print an advisory.
// ============================================================================

var claudeEnvDefaults = []struct{ key, value string }{
	{"CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY", "1"},
}

// claudeCodePresent reports whether Claude Code looks installed for this user.
func claudeCodePresent() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil && info.IsDir()
}

func configureClaudeCode(stdout io.Writer) {
	if os.Getenv("ZVK_NO_CLAUDE_MD") != "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "[zvk] note: could not locate Claude Code settings: %v\n", err)
		return
	}
	path := filepath.Join(home, ".claude", "settings.json")
	for _, d := range claudeEnvDefaults {
		changed, err := setClaudeEnvDefault(path, d.key, d.value)
		switch {
		case err != nil:
			fmt.Fprintf(stdout, "[zvk] note: could not set %s in %s: %v\n", d.key, path, err)
		case changed:
			fmt.Fprintf(stdout, "[zvk] Claude Code: set %s=%s in %s\n", d.key, d.value, path)
		}
	}
}

// jsonMember is one key/value pair of a JSON object, value kept verbatim.
type jsonMember struct {
	key   string
	value json.RawMessage
}

// setClaudeEnvDefault adds env[key]=value to the settings file unless the key
// is already present. It reports whether the file was changed.
func setClaudeEnvDefault(path, key, value string) (bool, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := os.FileMode(0o644)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	case os.IsNotExist(err):
		data = nil
	default:
		return false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}

	top, err := parseJSONObject(data)
	if err != nil {
		return false, fmt.Errorf("not a JSON object, leaving it unchanged: %w", err)
	}
	envIdx := -1
	for i, m := range top {
		if m.key == "env" {
			envIdx = i
		}
	}
	var env []jsonMember
	if envIdx >= 0 {
		if env, err = parseJSONObject(top[envIdx].value); err != nil {
			return false, fmt.Errorf("\"env\" is not a JSON object, leaving it unchanged: %w", err)
		}
		for _, m := range env {
			if m.key == key {
				return false, nil
			}
		}
	}
	v, _ := json.Marshal(value)
	env = append(env, jsonMember{key, v})
	envRaw, err := encodeJSONObject(env)
	if err != nil {
		return false, err
	}
	if envIdx >= 0 {
		top[envIdx].value = envRaw
	} else {
		top = append(top, jsonMember{"env", envRaw})
	}
	compact, err := encodeJSONObject(top)
	if err != nil {
		return false, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return false, err
	}
	out.WriteByte('\n')
	return true, writeFileAtomic(path, out.Bytes(), mode)
}

// parseJSONObject decodes a JSON object into its members in source order.
func parseJSONObject(data []byte) ([]jsonMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("expected an object")
	}
	var members []jsonMember
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected an object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		members = append(members, jsonMember{key, raw})
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the object")
	}
	return members, nil
}

func encodeJSONObject(members []jsonMember) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		var k bytes.Buffer
		enc := json.NewEncoder(&k)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m.key); err != nil {
			return nil, err
		}
		b.Write(bytes.TrimSuffix(k.Bytes(), []byte("\n")))
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ============================================================================
// Claude Code reference docs for an installed Zig.
//
// Zig has no stability promise — stdlib and the build system are reorganized
// across releases — and an LLM's training cut-off lags the current compiler.
// So after installing a Zig, zvk deterministically lays out the raw material an
// assistant needs to write code against *this* build instead of from stale
// memory:
//
//   <root>/zig/REFERENCE.<channel>.md          version-pinned pointers + topic map
//   <root>/zig/versions/<ver>/STD_INDEX.md     lightweight stdlib symbol index
//   <root>/zig/versions/<ver>/release-notes.html   official breaking-change list (snapshot)
//   <root>/zig/versions/<ver>/ADAPTATION.prompt.md a prompt that produces ADAPTATION.md
//   <root>/zig/CLAUDE.md                        decision table, @import'd into ~/.claude/CLAUDE.md
//
// zvk never calls an LLM: it stages authoritative inputs (notes, index) and a
// prompt template; understanding the diff is left to the assistant in-session
// (it writes ADAPTATION.md from the prompt). Everything here is best-effort —
// a failure prints an advisory and never fails the install.
//
// Disable with ZVK_NO_DOCS (skip all of it) or ZVK_NO_CLAUDE_MD (keep the
// reference docs but don't write/inject CLAUDE.md).
// ============================================================================

// zigDocNote prints a non-fatal advisory; doc generation must not break install.
func zigDocNote(stdout io.Writer, format string, args ...any) {
	fmt.Fprintf(stdout, "[zvk zig] note: "+format+"\n", args...)
}

func writeZigDocs(root, version, channel string, stdout io.Writer) {
	if os.Getenv("ZVK_NO_DOCS") != "" {
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	versionDir := zigDirs.versionDir(absRoot, version)
	stdRoot := filepath.Join(versionDir, "lib", "std")
	zigRoot := filepath.Join(absRoot, "zig")

	// release-notes.html — official breaking-change list, snapshotted locally.
	// Only tagged releases publish one; a nightly ("0.X.Y-dev.N+hash") has no
	// release-notes.html yet (the would-be URL 404s), so skip the fetch rather
	// than emit a scary advisory for an expected gap. Nightly's REFERENCE leans
	// on the master docs + recent std commits instead.
	notesPath := filepath.Join(versionDir, "release-notes.html")
	notesURL := zigReleaseNotesURL(version)
	notesLocal := false
	if channel != "nightly" {
		if data, err := downloadToMemory(notesURL); err != nil {
			zigDocNote(stdout, "could not fetch release notes (%v); REFERENCE keeps the online URL", err)
		} else if err := writeFileAtomic(notesPath, data, 0o644); err != nil {
			zigDocNote(stdout, "could not save release notes: %v", err)
		} else {
			notesLocal = true
		}
	}

	// STD_INDEX.md — lightweight symbol index over the stdlib top level.
	indexPath := filepath.Join(versionDir, "STD_INDEX.md")
	if idx, err := buildStdIndex(version, stdRoot); err != nil {
		zigDocNote(stdout, "could not index stdlib: %v", err)
	} else if err := writeFileAtomic(indexPath, []byte(idx), 0o644); err != nil {
		zigDocNote(stdout, "could not write STD_INDEX.md: %v", err)
	}

	// ADAPTATION.prompt.md — the trigger for the in-session intelligence layer.
	promptPath := filepath.Join(versionDir, "ADAPTATION.prompt.md")
	if err := writeFileAtomic(promptPath, []byte(adaptationPrompt(version, versionDir, notesLocal)), 0o644); err != nil {
		zigDocNote(stdout, "could not write ADAPTATION.prompt.md: %v", err)
	}

	// REFERENCE.<channel>.md — version-pinned pointers + grep topic map.
	refPath := filepath.Join(zigRoot, "REFERENCE."+channel+".md")
	ref := zigReference(channel, version, versionDir, stdRoot, notesPath, notesURL, indexPath, promptPath, notesLocal)
	if err := writeFileAtomic(refPath, []byte(ref), 0o644); err != nil {
		zigDocNote(stdout, "could not write %s: %v", filepath.Base(refPath), err)
	}

	// CLAUDE.md + global @import — make the reference discoverable to the assistant.
	if os.Getenv("ZVK_NO_CLAUDE_MD") == "" {
		claudePath := filepath.Join(zigRoot, "CLAUDE.md")
		if err := writeFileAtomic(claudePath, []byte(zigClaudeMd(absRoot)), 0o644); err != nil {
			zigDocNote(stdout, "could not write zig CLAUDE.md: %v", err)
		} else if err := injectGlobalImport(claudePath); err != nil {
			zigDocNote(stdout, "could not link CLAUDE.md into ~/.claude/CLAUDE.md: %v", err)
		}
	}

	fmt.Fprintf(stdout, "[zvk zig] Claude Code reference docs written under %s\n", zigRoot)
	fmt.Fprintf(stdout, "[zvk zig] to generate version-adaptation notes, have Claude run the prompt in:\n        %s\n", promptPath)
}

// zigReleaseNotesURL maps a version to its official release-notes URL. Release
// uses its own version; nightly ("0.17.0-dev.356+...") points at the in-progress
// notes for that minor (everything before the first "-").
func zigReleaseNotesURL(version string) string {
	notesVer := version
	if i := strings.IndexByte(version, '-'); i >= 0 {
		notesVer = version[:i]
	}
	return "https://ziglang.org/download/" + notesVer + "/release-notes.html"
}

// ----------------------------------------------------------------------------
// stdlib symbol index
// ----------------------------------------------------------------------------

// buildStdIndex scans the top level of lib/std and, for each .zig file, lists
// its column-0 `pub fn` / `pub const` declarations. It deliberately does NOT
// recurse or descend into nested scopes — the full tree has ~25k pub decls,
// which is useless as a single document. Subdirectories are listed as grep
// entry points.
func buildStdIndex(version, stdRoot string) (string, error) {
	entries, err := os.ReadDir(stdRoot)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var b strings.Builder
	fmt.Fprintf(&b, "# Zig %s — stdlib top-level index\n\n", version)
	b.WriteString("Auto-generated by zvk. Do not edit.\n\n")
	b.WriteString("Column-0 `pub` declarations per top-level `lib/std` file. This is a\n")
	b.WriteString("coarse \"which file exports what\" map, not a full symbol table — grep the\n")
	b.WriteString("listed file (or descend into the listed subdirectory) for the details.\n\n")
	fmt.Fprintf(&b, "stdlib root: `%s`\n\n", stdRoot)

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".zig") {
			continue
		}
		syms, err := topLevelPubSymbols(filepath.Join(stdRoot, name))
		if err != nil || len(syms) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", name)
		fmt.Fprintf(&b, "%s\n\n", strings.Join(syms, ", "))
	}

	if len(dirs) > 0 {
		b.WriteString("## Subdirectories (grep entry points)\n")
		for _, d := range dirs {
			fmt.Fprintf(&b, "- `%s/`\n", d)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// topLevelPubSymbols returns the names declared by column-0 `pub fn` / `pub
// const` lines in a .zig file (indentation means nested scope — skipped).
func topLevelPubSymbols(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		var rest string
		switch {
		case strings.HasPrefix(line, "pub fn "):
			rest = line[len("pub fn "):]
		case strings.HasPrefix(line, "pub const "):
			rest = line[len("pub const "):]
		default:
			continue
		}
		if name := leadingIdent(rest); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// leadingIdent reads the identifier at the start of s (up to the first char that
// can't be part of an identifier).
func leadingIdent(s string) string {
	end := 0
	for end < len(s) {
		c := s[end]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			end++
			continue
		}
		break
	}
	return s[:end]
}

// ----------------------------------------------------------------------------
// REFERENCE.<channel>.md
// ----------------------------------------------------------------------------

func zigReference(channel, version, versionDir, stdRoot, notesPath, notesURL, indexPath, promptPath string, notesLocal bool) string {
	compiler := filepath.Join(versionDir, zigExeName())
	langref := filepath.Join(versionDir, "doc", "langref.html")

	var b strings.Builder
	fmt.Fprintf(&b, "# `%s` — %s %s\n\n", zigCmdForChannel(channel), channel, version)
	b.WriteString("Auto-generated by zvk. Do not edit.\n\n")

	b.WriteString("## When to use\n\n")
	if channel == "nightly" {
		b.WriteString("Use this when a project's `build.zig.zon` `minimum_zig_version` is a\n")
		b.WriteString("`0.X.Y-dev.NNN+HASH` string higher than the installed release.\n\n")
		b.WriteString("## ⚠ std drifts daily\n\n")
		b.WriteString("Nightly std API changes between rebuilds — names, signatures, and modules\n")
		b.WriteString("are reorganized without notice. The **local** stdlib is the only source\n")
		b.WriteString("guaranteed to match the compiler being invoked. Online docs may already be\n")
		b.WriteString("ahead of (or behind) this exact build.\n\n")
	} else {
		b.WriteString("Use this when a project's `build.zig.zon` `minimum_zig_version` is ≤ the\n")
		b.WriteString("version above. The API is frozen for this release — online docs and the\n")
		b.WriteString("local stdlib agree.\n\n")
	}

	b.WriteString("## Local references (authoritative for this build)\n\n")
	fmt.Fprintf(&b, "- Compiler: `%s`\n", compiler)
	fmt.Fprintf(&b, "- Language reference: `%s`\n", langref)
	fmt.Fprintf(&b, "- stdlib source root: `%s`\n", stdRoot)
	fmt.Fprintf(&b, "- stdlib symbol index: `%s`\n", indexPath)
	if notesLocal {
		fmt.Fprintf(&b, "- Release notes (local snapshot): `%s`\n", notesPath)
	}
	fmt.Fprintf(&b, "- Version-adaptation prompt: `%s`\n", promptPath)
	b.WriteString("\n")

	b.WriteString("## stdlib topic map (grep starting points)\n\n")
	for _, t := range zigTopicMap(stdRoot) {
		fmt.Fprintf(&b, "- %-13s → %s\n", t.topic, t.paths)
	}
	b.WriteString("\n")

	b.WriteString("## Online references\n\n")
	docVer := version
	if channel == "nightly" {
		docVer = "master"
	}
	fmt.Fprintf(&b, "- std docs: https://ziglang.org/documentation/%s/std/\n", docVer)
	fmt.Fprintf(&b, "- Language ref: https://ziglang.org/documentation/%s/\n", docVer)
	if channel == "nightly" {
		// The tagged notes for this minor don't exist until it ships, so the URL
		// 404s for a dev build. Flag that and point at master commits — the
		// authoritative, always-available "what changed" source for nightly.
		fmt.Fprintf(&b, "- Release notes (in-development; 404s until this minor is released): %s\n", notesURL)
		b.WriteString("- Recent std commits (diagnose \"broke since last update\"):\n")
		b.WriteString("  https://github.com/ziglang/zig/commits/master/lib/std\n")
	} else {
		fmt.Fprintf(&b, "- Release notes: %s\n", notesURL)
	}
	b.WriteString("\n")

	if channel == "nightly" {
		b.WriteString("## Known volatile areas\n\n")
		b.WriteString("- `std.Io` — Reader/Writer, File, Dir reorganized\n")
		b.WriteString("- `std.process` — env/args API\n")
		b.WriteString("- Build system — `b.createModule`, root_module field\n")
		b.WriteString("- HTTP client — moved under `std.Io`\n\n")
	}
	return b.String()
}

type zigTopic struct{ topic, paths string }

// zigTopicMap returns absolute grep starting points per stdlib topic.
func zigTopicMap(stdRoot string) []zigTopic {
	p := func(parts ...string) string { return filepath.Join(append([]string{stdRoot}, parts...)...) }
	pair := func(a, b string) string { return a + "`, `" + b }
	return []zigTopic{
		{"I/O", "`" + pair(p("Io.zig"), p("Io")) + "`"},
		{"Filesystem", "`" + pair(p("fs.zig"), p("fs")) + "`"},
		{"HTTP", "`" + pair(p("http.zig"), p("http")) + "`"},
		{"Process/env", "`" + p("process.zig") + "`"},
		{"Build system", "`" + pair(p("Build.zig"), p("Build")) + "`"},
		{"Crypto", "`" + p("crypto") + "`"},
		{"Compression", "`" + pair(p("compress"), p("tar.zig")) + "`"},
		{"JSON", "`" + p("json") + "`"},
		{"Formatting", "`" + p("fmt.zig") + "`"},
		{"Containers", "`" + pair(p("array_list.zig"), p("hash_map.zig")) + "`"},
		{"Allocators", "`" + pair(p("heap"), p("mem", "Allocator.zig")) + "`"},
		{"Threading", "`" + p("Thread") + "`"},
	}
}

// ----------------------------------------------------------------------------
// ADAPTATION.prompt.md
// ----------------------------------------------------------------------------

func adaptationPrompt(version, versionDir string, notesLocal bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Adaptation prompt — Zig %s\n\n", version)
	b.WriteString("Auto-generated by zvk. Do not edit. Feed this to Claude when you want a\n")
	b.WriteString("version-adaptation cheat sheet for this exact Zig build.\n\n")
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "You are adapting to Zig %s. Read these first:\n\n", version)
	// A dev build has no local release-notes snapshot (the page 404s until the
	// minor ships), so send Claude to the always-current master commit log — the
	// authoritative change source for nightly — instead of a missing file.
	if notesLocal {
		fmt.Fprintf(&b, "- `%s` — official breaking-change list for this version\n", filepath.Join(versionDir, "release-notes.html"))
	} else {
		b.WriteString("- https://github.com/ziglang/zig/commits/master/lib/std — recent std changes (no published notes for a dev build; this is the authoritative change source)\n")
	}
	fmt.Fprintf(&b, "- `%s` — which stdlib file exports what\n", filepath.Join(versionDir, "STD_INDEX.md"))
	fmt.Fprintf(&b, "- stdlib source under `%s` — grep to confirm exact signatures\n\n", filepath.Join(versionDir, "lib", "std"))
	b.WriteString("Then write a file `ADAPTATION.md` next to this prompt containing, concisely:\n\n")
	b.WriteString("1. **Outdated in my memory** — APIs/idioms you (the model) are likely to\n")
	b.WriteString("   reach for that are renamed, moved, or removed in this version. For each,\n")
	b.WriteString("   give the wrong form → the correct form, verified against the local stdlib.\n")
	b.WriteString("2. **High-risk areas** — modules most likely to bite (I/O, allocators, build\n")
	b.WriteString("   system), with the current canonical usage.\n")
	b.WriteString("3. **Quick idioms** — a few copy-pasteable snippets that compile on this build\n")
	b.WriteString("   (allocator setup, ArrayList, reading a file, a minimal build.zig).\n\n")
	b.WriteString("Cite the local file/line you verified each claim against. If you cannot\n")
	b.WriteString("verify a claim locally, mark it UNVERIFIED rather than guessing.\n")
	return b.String()
}

// ----------------------------------------------------------------------------
// CLAUDE.md + global @import injection
// ----------------------------------------------------------------------------

func zigCmdForChannel(channel string) string {
	if channel == "nightly" {
		return "zig-nightly"
	}
	return "zig"
}

// zigClaudeMd is the cross-channel pointer loaded into the assistant's context:
// a decision rule plus links to each channel's REFERENCE.
func zigClaudeMd(absRoot string) string {
	zigRoot := filepath.Join(absRoot, "zig")
	relVer, _ := zigDirs.readActive(absRoot, "release")
	nightlyVer, _ := zigDirs.readActive(absRoot, "nightly")
	cell := func(v string) string {
		if v == "" {
			return "(not installed)"
		}
		return v
	}

	var b strings.Builder
	b.WriteString("# Zig environment (managed by zvk)\n\n")
	b.WriteString("Auto-generated by zvk. Do not edit — overwritten on next `zvk zig` command.\n")
	b.WriteString("Disable with `ZVK_NO_CLAUDE_MD=1`.\n\n")
	b.WriteString("## Which command to invoke\n\n")
	b.WriteString("| Command       | Version           | Channel |\n")
	b.WriteString("|---------------|-------------------|---------|\n")
	fmt.Fprintf(&b, "| `zig`         | %-17s | release |\n", cell(relVer))
	fmt.Fprintf(&b, "| `zig-nightly` | %-17s | nightly |\n", cell(nightlyVer))
	b.WriteString("\n")
	b.WriteString("**Decision rule**: read a project's `build.zig.zon` `minimum_zig_version`.\n")
	b.WriteString("If it is ≤ the `zig` row, use bare `zig`. If higher (a `0.X.Y-dev.NNN+HASH`\n")
	b.WriteString("string), use `zig-nightly`.\n\n")
	b.WriteString("## Deeper references (read on demand)\n\n")
	if relVer != "" {
		fmt.Fprintf(&b, "- Release: `%s`\n", filepath.Join(zigRoot, "REFERENCE.release.md"))
	}
	if nightlyVer != "" {
		fmt.Fprintf(&b, "- Nightly: `%s`\n", filepath.Join(zigRoot, "REFERENCE.nightly.md"))
	}
	b.WriteString("\nEach REFERENCE points at the local stdlib, symbol index, release-notes\n")
	b.WriteString("snapshot, and an adaptation prompt for that build.\n")
	return b.String()
}

// injectGlobalImport idempotently appends `@<claudePath>` to ~/.claude/CLAUDE.md
// so the assistant auto-loads the zig pointer. Mirrors pathenv.go's approach:
// search for the line before appending to avoid duplicate writes.
func injectGlobalImport(claudePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	globalPath := filepath.Join(home, ".claude", "CLAUDE.md")
	importLine := "@" + claudePath

	existing, err := os.ReadFile(globalPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), importLine) {
		return nil
	}
	var buf []byte
	buf = append(buf, existing...)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf = append(buf, '\n')
	}
	buf = append(buf, []byte("\n# Added by zvk\n"+importLine+"\n")...)
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(globalPath, buf, 0o644)
}

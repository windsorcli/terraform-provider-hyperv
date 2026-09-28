package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferencesIssueNumber(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"option #1 is preferred", false},
		{"Step #2: restart the NIC", false},
		{"see PR #70 for context", true},
		{"fixed in (#36)", true},
		{"issue #20 tracks this", true},
		{"no numbers here", false},
	}
	for _, tc := range cases {
		if got := referencesIssueNumber(tc.text); got != tc.want {
			t.Errorf("referencesIssueNumber(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestLintGoFile_ExemptionMustBeLastLine(t *testing.T) {
	body := strings.Repeat("// filler line\n", maxBlockLines)

	markerFirst := "package p\n\n// lint:allow-long-comment\n" + body + "func F() {}\n"
	markerLast := "package p\n\n" + body + "// lint:allow-long-comment\nfunc F() {}\n"

	dir := t.TempDir()
	pathFirst := filepath.Join(dir, "first.go")
	pathLast := filepath.Join(dir, "last.go")
	if err := os.WriteFile(pathFirst, []byte(markerFirst), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathLast, []byte(markerLast), 0o644); err != nil {
		t.Fatal(err)
	}

	vFirst, err := lintGoFile(pathFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(vFirst) == 0 {
		t.Error("marker on first line: want a block-length violation, got none")
	}

	vLast, err := lintGoFile(pathLast)
	if err != nil {
		t.Fatal(err)
	}
	if len(vLast) != 0 {
		t.Errorf("marker on last line: want no violations, got %v", vLast)
	}
}

func TestLintPS1File_ExemptionMustBeLastLine(t *testing.T) {
	body := strings.Repeat("# filler line\n", maxBlockLines)

	markerFirst := "# lint:allow-long-comment\n" + body + "Write-Host 'hi'\n"
	markerLast := body + "# lint:allow-long-comment\nWrite-Host 'hi'\n"

	dir := t.TempDir()
	pathFirst := filepath.Join(dir, "first.ps1")
	pathLast := filepath.Join(dir, "last.ps1")
	if err := os.WriteFile(pathFirst, []byte(markerFirst), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathLast, []byte(markerLast), 0o644); err != nil {
		t.Fatal(err)
	}

	vFirst, err := lintPS1File(pathFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(vFirst) == 0 {
		t.Error("marker on first line: want a block-length violation, got none")
	}

	vLast, err := lintPS1File(pathLast)
	if err != nil {
		t.Fatal(err)
	}
	if len(vLast) != 0 {
		t.Errorf("marker on last line: want no violations, got %v", vLast)
	}
}

func TestLintPS1File_BlockCommentCloserNotCountedAsLineComment(t *testing.T) {
	content := "<#\n.SYNOPSIS\nfoo\n#>\n" + strings.Repeat("# filler line\n", maxBlockLines) + "Write-Host 'hi'\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "closer.ps1")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := lintPS1File(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Errorf("#> should not be swept into the following comment block, got violations: %v", v)
	}
}

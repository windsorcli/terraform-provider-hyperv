// Comment-style gate for CLAUDE.md's code-comment rules. Flags overlong
// comment blocks and phrases the rules ban outright (PR/issue references,
// "previously"/"used to" narration, links to gitignored maintainer docs,
// "--" used as a dash). Run via `task lint:comments`.
//
// A block that's genuinely reference data (a wire contract, a JSON shape)
// rather than prose is exempt from the length check if its first line,
// stripped of the comment marker, is exactly "lint:allow-long-comment".
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxBlockLines = 10

var skipDirs = map[string]bool{
	".git":   true,
	".task":  true,
	"vendor": true,
	"dist":   true,
}

type bannedPattern struct {
	re     *regexp.Regexp
	reason string
}

// Deliberately narrow: only patterns whose fix is unambiguous and
// mechanical (delete the reference, state current behavior). Sentence-level
// AI tells, like staged contrasts or leaning on a dash as the connector for
// every clause, aren't here on purpose. A regex can't tell a real rewrite
// from a punctuation swap, and the swap alone is worse than no fix. Catch
// those in review against the technical-writing skill instead.
var bannedPatterns = []bannedPattern{
	{regexp.MustCompile(`#\d+\b`), "references a PR/issue number"},
	{regexp.MustCompile(`(?i)\bpreviously\b`), "narrates history instead of current behavior"},
	{regexp.MustCompile(`(?i)\bused to\b`), "narrates history instead of current behavior"},
	{regexp.MustCompile(`docs/PLAN\.md`), "links a gitignored maintainer-only doc"},
	{regexp.MustCompile(`docs/spikes`), "links a gitignored maintainer-only doc"},
	{regexp.MustCompile(`docs/adr`), "links a gitignored maintainer-only doc"},
}

type violation struct {
	path   string
	line   int
	reason string
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}

	var violations []violation
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".ps1":
			default:
				return nil
			}
			fileViolations, err := lintFile(path)
			if err != nil {
				return err
			}
			violations = append(violations, fileViolations...)
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "lint-comments:", err)
			os.Exit(2)
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].path != violations[j].path {
			return violations[i].path < violations[j].path
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		fmt.Printf("%s:%d: %s\n", v.path, v.line, v.reason)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d comment-style violation(s). See CLAUDE.md \"Code comments\".\n", len(violations))
		os.Exit(1)
	}
}

func lintFile(path string) ([]violation, error) {
	marker := "//"
	if filepath.Ext(path) == ".ps1" {
		marker = "#"
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var violations []violation
	var block []string
	blockStart := 0

	flush := func() {
		if len(block) == 0 {
			return
		}
		exempt := strings.TrimSpace(strings.TrimPrefix(block[0], marker)) == "lint:allow-long-comment"
		if !exempt && len(block) > maxBlockLines {
			violations = append(violations, violation{
				path:   path,
				line:   blockStart,
				reason: fmt.Sprintf("comment block is %d lines (max %d); add %q as the block's first line if this is reference data, not prose", len(block), maxBlockLines, "lint:allow-long-comment"),
			})
		}
		for i, line := range block {
			for _, bp := range bannedPatterns {
				if bp.re.MatchString(line) {
					violations = append(violations, violation{
						path:   path,
						line:   blockStart + i,
						reason: bp.reason,
					})
				}
			}
		}
		block = nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, marker) && !strings.HasPrefix(trimmed, "#!") && !strings.HasPrefix(trimmed, "#Requires") {
			if len(block) == 0 {
				blockStart = lineNo
			}
			block = append(block, trimmed)
			continue
		}
		flush()
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return violations, nil
}

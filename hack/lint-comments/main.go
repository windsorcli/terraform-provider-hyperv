// Comment-style gate for CLAUDE.md's code-comment rules. Flags overlong
// comment blocks, multi-line comments inside a function body, and
// banned phrases: PR/issue references, historical narration, and links
// to gitignored maintainer docs. Run via `task lint:comments`.
//
// A doc comment that's genuinely reference data (a wire contract, a
// JSON schema) is exempt from the length check if one of its lines,
// stripped of the marker, reads exactly "lint:allow-long-comment",
// placed last so it doesn't become the godoc synopsis. There's no such
// exemption inside a function body: those are capped at one line, always.
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
	{regexp.MustCompile(`(?i)\bpreviously\b`), "narrates history instead of current behavior"},
	{regexp.MustCompile(`(?i)\bused to\b`), "narrates history instead of current behavior"},
	{regexp.MustCompile(`docs/PLAN\.md`), "links a gitignored maintainer-only doc"},
	{regexp.MustCompile(`docs/spikes`), "links a gitignored maintainer-only doc"},
	{regexp.MustCompile(`docs/adr`), "links a gitignored maintainer-only doc"},
	{regexp.MustCompile(`(?i)\bshape\b`), `"shape" is a lazy stand-in; name the actual noun (format, structure, contract, schema, layout)`},
}

type violation struct {
	path   string
	line   int
	reason string
}

var issueRefPattern = regexp.MustCompile(`(\w+)?[ \t]*#(\d+)\b`)

// ordinalWords precede "#N" as an enumerator, not a PR/issue citation
// (e.g. "option #1", "Step #2"); RE2 has no lookbehind, so
// referencesIssueNumber filters on the captured word instead.
var ordinalWords = map[string]bool{
	"step": true, "option": true, "item": true, "case": true,
	"slot": true, "phase": true, "priority": true, "line": true,
	"figure": true, "table": true, "note": true, "rule": true,
}

// referencesIssueNumber reports whether text cites a PR or issue
// number, as opposed to an ordinal like "step #2".
func referencesIssueNumber(text string) bool {
	for _, m := range issueRefPattern.FindAllStringSubmatch(text, -1) {
		if !ordinalWords[strings.ToLower(m[1])] {
			return true
		}
	}
	return false
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}

	var violations []violation
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { // #nosec G703 -- root is a CLI arg to this local dev tool, not untrusted input
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			var fileViolations []violation
			switch filepath.Ext(path) {
			case ".go":
				fileViolations, err = lintGoFile(path)
			case ".ps1":
				fileViolations, err = lintPS1File(path)
			default:
				return nil
			}
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

func isExemptLine(text string) bool {
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimPrefix(text, "#")
	return strings.TrimSpace(text) == "lint:allow-long-comment"
}

func checkBanned(path string, lineNo int, text string) []violation {
	var violations []violation
	if referencesIssueNumber(text) {
		violations = append(violations, violation{path: path, line: lineNo, reason: "references a PR/issue number"})
	}
	for _, bp := range bannedPatterns {
		if bp.re.MatchString(text) {
			violations = append(violations, violation{path: path, line: lineNo, reason: bp.reason})
		}
	}
	return violations
}

// lintGoFile parses path's AST so it can tell a doc comment (attached to
// a package, type, func, var, or field) from a comment inside a function
// or closure body. Doc comments get the shared length backstop and its
// escape hatch; body comments are capped at one line, no exceptions.
func lintGoFile(path string) ([]violation, error) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var bodies []*ast.BlockStmt
	ast.Inspect(astFile, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				bodies = append(bodies, fn.Body)
			}
		case *ast.FuncLit:
			if fn.Body != nil {
				bodies = append(bodies, fn.Body)
			}
		}
		return true
	})
	inBody := func(pos token.Pos) bool {
		for _, b := range bodies {
			if pos > b.Lbrace && pos < b.Rbrace {
				return true
			}
		}
		return false
	}

	var violations []violation
	for _, cg := range astFile.Comments {
		var lines []string
		var lineNos []int
		for _, c := range cg.List {
			for i, l := range strings.Split(c.Text, "\n") {
				lines = append(lines, l)
				lineNos = append(lineNos, fset.Position(c.Slash).Line+i)
			}
		}
		if len(lines) == 0 {
			continue
		}

		if inBody(cg.Pos()) {
			if len(lines) > 1 {
				violations = append(violations, violation{
					path:   path,
					line:   lineNos[0],
					reason: fmt.Sprintf("comment inside a function body is %d lines; one line max, always", len(lines)),
				})
			}
		} else {
			exempt := isExemptLine(lines[len(lines)-1])
			if !exempt && len(lines) > maxBlockLines {
				violations = append(violations, violation{
					path:   path,
					line:   lineNos[0],
					reason: fmt.Sprintf("comment block is %d lines (max %d); add a %q line (last line, so it doesn't become the godoc synopsis) if this is reference data, not prose", len(lines), maxBlockLines, "lint:allow-long-comment"),
				})
			}
		}

		for i, l := range lines {
			violations = append(violations, checkBanned(path, lineNos[i], l)...)
		}
	}
	return violations, nil
}

func lintPS1File(path string) ([]violation, error) {
	f, err := os.Open(path) // #nosec G304 -- path is discovered by walking a CLI-supplied root, not untrusted input
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
		exempt := isExemptLine(block[len(block)-1])
		if !exempt && len(block) > maxBlockLines {
			violations = append(violations, violation{
				path:   path,
				line:   blockStart,
				reason: fmt.Sprintf("comment block is %d lines (max %d); add a %q line (last line) if this is reference data, not prose", len(block), maxBlockLines, "lint:allow-long-comment"),
			})
		}
		for i, line := range block {
			violations = append(violations, checkBanned(path, blockStart+i, line)...)
		}
		block = nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#!") &&
			!strings.HasPrefix(trimmed, "#Requires") && !strings.HasPrefix(trimmed, "#>") {
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

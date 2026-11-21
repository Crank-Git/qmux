package qmux_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exampleMarker matches the line that names the Example function of the next Go block
// (FR-docs-17).
var exampleMarker = regexp.MustCompile(`^<!-- example: (\S+) -->$`)

// readmeBlock is one Go block of a README file.
type readmeBlock struct {
	marker string // The name in the marker line, or "" when the line is not a marker.
	lines  []string
}

// readmeGoBlocks returns the Go blocks of readme in file order.
func readmeGoBlocks(readme string) []readmeBlock {
	var blocks []readmeBlock
	lines := strings.Split(readme, "\n")
	for i := 0; i < len(lines); i++ {
		if lines[i] != "```go" {
			continue
		}
		var b readmeBlock
		if i > 0 {
			if m := exampleMarker.FindStringSubmatch(lines[i-1]); m != nil {
				b.marker = m[1]
			}
		}
		for i++; i < len(lines) && lines[i] != "```"; i++ {
			b.lines = append(b.lines, lines[i])
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// exampleBodies returns the body lines of each Example function in the Go file at path.
// Each body loses one leading tab per line and its "// Output:" comment.
func exampleBodies(path string) (map[string][]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read examples: %w", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse examples: %w", err)
	}
	bodies := make(map[string][]string)
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Example") {
			continue
		}
		start := fset.Position(fn.Body.Lbrace).Offset + 1
		end := fset.Position(fn.Body.Rbrace).Offset
		// The Output comment is the last text of an Example body, so the body ends
		// where that comment starts.
		for _, c := range f.Comments {
			off := fset.Position(c.Pos()).Offset
			if off > start && off < end && isOutputComment(c.List[0].Text) {
				end = off
			}
		}
		bodies[fn.Name.Name] = trimBody(string(src[start:end]))
	}
	return bodies, nil
}

// isOutputComment reports whether text opens an Output comment of a testable example.
func isOutputComment(text string) bool {
	t := strings.TrimSpace(strings.TrimPrefix(text, "//"))
	return strings.HasPrefix(t, "Output:") || strings.HasPrefix(t, "Unordered output:")
}

// trimBody removes one leading tab from each line of an Example body, because a
// function body has one more indent level than a README block. It then removes the
// blank lines at the start and the end. The spec permits no other change.
func trimBody(body string) []string {
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		lines = append(lines, strings.TrimPrefix(l, "\t"))
	}
	return trimBlankLines(lines)
}

// trimBlankLines removes the blank lines at the start and the end of lines.
func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// readmeProblems returns one message per README Go block that has no marker line,
// names a missing Example function, or differs from the body of its Example function.
func readmeProblems(readmePath, examplesPath string) ([]string, error) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("read readme: %w", err)
	}
	bodies, err := exampleBodies(examplesPath)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(readmePath)
	var problems []string
	for i, b := range readmeGoBlocks(string(readme)) {
		n := i + 1
		body, ok := bodies[b.marker]
		switch {
		case b.marker == "":
			problems = append(problems, fmt.Sprintf("%s block %d has no <!-- example: ... --> line", name, n))
		case !ok:
			problems = append(problems, fmt.Sprintf("%s block %d names %s, which is not in %s", name, n, b.marker, filepath.Base(examplesPath)))
		default:
			if d := firstDifference(trimBlankLines(b.lines), body); d != "" {
				problems = append(problems, fmt.Sprintf("%s block %d differs from %s: %s", name, n, b.marker, d))
			}
		}
	}
	return problems, nil
}

// firstDifference describes the first line where readme and example differ, or
// returns "" when they are equal.
func firstDifference(readme, example []string) string {
	for i := range max(len(readme), len(example)) {
		var r, e string
		if i < len(readme) {
			r = readme[i]
		}
		if i < len(example) {
			e = example[i]
		}
		if r != e || i >= len(readme) || i >= len(example) {
			return fmt.Sprintf("line %d: README %q, Example %q", i+1, r, e)
		}
	}
	return ""
}

// readmeCopy writes readme to README.md in a temporary directory and returns its path.
func readmeCopy(t *testing.T, readme []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, readme, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// readReadme returns the README of the repository.
func readReadme(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return b
}

// wantProblem fails t unless the README at path gives exactly one problem that holds want.
func wantProblem(t *testing.T, path, want string) {
	t.Helper()
	problems, err := readmeProblems(path, "example_test.go")
	if err != nil {
		t.Fatalf("readmeProblems: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], want) {
		t.Fatalf("problems = %q, want one problem that holds %q", problems, want)
	}
}

func TestReadmeExamples(t *testing.T) { // FR-docs-16, FR-docs-17, FR-docs-18, FR-docs-19
	problems, err := readmeProblems("README.md", "example_test.go")
	if err != nil {
		t.Fatalf("readmeProblems: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	if n := len(readmeGoBlocks(string(readReadme(t)))); n == 0 {
		t.Error("README.md has no Go block")
	}
}

func TestReadmeExamplesFailWhenABlockLineChanges(t *testing.T) { // FR-docs-18
	readme := readReadme(t)
	old := []byte("\tZones:   []string{\"fleet.example.\"},\n")
	if !bytes.Contains(readme, old) {
		t.Fatalf("README.md has no line %q to change", old)
	}
	changed := bytes.Replace(readme, old, []byte("\tZones:   []string{\"other.example.\"},\n"), 1)
	wantProblem(t, readmeCopy(t, changed), "block 1 differs from ExampleNewServer")
}

func TestReadmeExamplesFailWhenABlockHasNoMarker(t *testing.T) { // FR-docs-19
	readme := readReadme(t)
	old := []byte("<!-- example: ExampleNewClient -->\n")
	if !bytes.Contains(readme, old) {
		t.Fatalf("README.md has no line %q to remove", old)
	}
	unmarked := bytes.Replace(readme, old, nil, 1)
	wantProblem(t, readmeCopy(t, unmarked), "block 2 has no <!-- example: ... --> line")
}

func TestReadmeExamplesFailWhenAMarkerNamesNoExample(t *testing.T) {
	readme := readReadme(t)
	renamed := bytes.Replace(readme, []byte("<!-- example: ExampleNewClient -->"), []byte("<!-- example: ExampleNoSuchFunction -->"), 1)
	wantProblem(t, readmeCopy(t, renamed), "names ExampleNoSuchFunction")
}

func TestExampleBodiesDropTheOutputComment(t *testing.T) {
	src := "package x_test\n\nimport \"fmt\"\n\nfunc ExampleHello() {\n\tfmt.Println(\"hello\")\n\n\t// Output:\n\t// hello\n}\n"
	path := filepath.Join(t.TempDir(), "example_test.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bodies, err := exampleBodies(path)
	if err != nil {
		t.Fatalf("exampleBodies: %v", err)
	}
	want := []string{`fmt.Println("hello")`}
	if d := firstDifference(bodies["ExampleHello"], want); d != "" {
		t.Errorf("ExampleHello body differs: %s", d)
	}
}

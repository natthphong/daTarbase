// Command dbtool maintains the problem set.
//
//	go run ./tools/dbtool readme    # regenerate README.md (problem index with links)
//	go run ./tools/dbtool progress  # run each problem's tests, print what you solved
//	go run ./tools/dbtool check     # lint problem markers / test naming
//	go run ./tools/dbtool template  # strip solutions → panic("TODO") (maintainers only)
//
// A problem is any func whose doc comment starts with a marker line:
//
//	// Problem 2.3 — InsertTuple [Medium]
//
// and its tests are the funcs named TestP2_3_* in the same phase directory.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var markerRE = regexp.MustCompile(`^Problem (\d+)\.(\d+) [—-] (\S+) \[(Easy|Medium|Hard)\]\s*$`)

type problem struct {
	Phase, Num int
	Name, Diff string
	Summary    string
	File       string // repo-relative
	Line       int
	TestFile   string
	TestLine   int
	Tests      int
}

func (p problem) ID() string         { return fmt.Sprintf("%d.%d", p.Phase, p.Num) }
func (p problem) TestPrefix() string { return fmt.Sprintf("TestP%d_%d_", p.Phase, p.Num) }

type phase struct {
	Num         int
	Dir         string
	Title       string
	Description string
	Problems    []*problem
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dbtool readme|progress|check|template")
		os.Exit(2)
	}
	phases, err := scan()
	if err != nil {
		fail(err)
	}
	switch os.Args[1] {
	case "readme":
		err = writeReadme(phases)
	case "progress":
		err = progress(phases)
	case "check":
		err = check(phases)
	case "template":
		err = template(phases)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dbtool:", err)
	os.Exit(1)
}

var phaseDirRE = regexp.MustCompile(`^phase(\d+)_\w+$`)

func scan() ([]*phase, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var phases []*phase
	for _, e := range entries {
		m := phaseDirRE.FindStringSubmatch(e.Name())
		if !e.IsDir() || m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		ph := &phase{Num: n, Dir: e.Name()}
		readPhaseReadme(ph)
		if err := scanPhase(ph); err != nil {
			return nil, err
		}
		phases = append(phases, ph)
	}
	sort.Slice(phases, func(i, j int) bool { return phases[i].Num < phases[j].Num })
	return phases, nil
}

// readPhaseReadme takes "# Phase N — Title" and the first paragraph after it.
func readPhaseReadme(ph *phase) {
	b, err := os.ReadFile(filepath.Join(ph.Dir, "README.md"))
	if err != nil {
		ph.Title = ph.Dir
		return
	}
	lines := strings.Split(string(b), "\n")
	var para []string
	for i, l := range lines {
		if i == 0 {
			t := strings.TrimLeft(l, "# ")
			if idx := strings.IndexAny(t, "—-"); idx >= 0 && strings.HasPrefix(t, "Phase") {
				t = strings.TrimSpace(strings.TrimLeft(t[idx:], "—- "))
			}
			ph.Title = t
			continue
		}
		if strings.TrimSpace(l) == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(l, "#") {
			break
		}
		para = append(para, strings.TrimSpace(l))
	}
	ph.Description = strings.Join(para, " ")
}

func scanPhase(ph *phase) error {
	files, _ := filepath.Glob(filepath.Join(ph.Dir, "*.go"))
	sort.Strings(files)
	fset := token.NewFileSet()
	type testRef struct {
		file string
		line int
	}
	tests := map[string][]testRef{}
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		isTest := strings.HasSuffix(f, "_test.go")
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if isTest {
				if strings.HasPrefix(fd.Name.Name, "TestP") {
					parts := strings.SplitN(fd.Name.Name, "_", 3)
					if len(parts) == 3 {
						key := parts[0] + "_" + parts[1] + "_"
						tests[key] = append(tests[key], testRef{f, fset.Position(fd.Pos()).Line})
					}
				}
				continue
			}
			p := parseMarker(fd.Doc)
			if p == nil {
				continue
			}
			p.File = filepath.ToSlash(f)
			p.Line = fset.Position(fd.Doc.Pos()).Line
			ph.Problems = append(ph.Problems, p)
		}
	}
	for _, p := range ph.Problems {
		if refs := tests[p.TestPrefix()]; len(refs) > 0 {
			p.TestFile, p.TestLine, p.Tests = filepath.ToSlash(refs[0].file), refs[0].line, len(refs)
		}
	}
	sort.Slice(ph.Problems, func(i, j int) bool { return ph.Problems[i].Num < ph.Problems[j].Num })
	return nil
}

func parseMarker(doc *ast.CommentGroup) *problem {
	if doc == nil || len(doc.List) == 0 {
		return nil
	}
	lines := strings.Split(doc.Text(), "\n")
	m := markerRE.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if m == nil {
		return nil
	}
	ph, _ := strconv.Atoi(m[1])
	n, _ := strconv.Atoi(m[2])
	p := &problem{Phase: ph, Num: n, Name: m[3], Diff: m[4]}
	for _, l := range lines[1:] {
		if s := strings.TrimSpace(l); s != "" {
			p.Summary = s
			break
		}
	}
	return p
}

// ---------------------------------------------------------------- readme

func writeReadme(phases []*phase) error {
	var b bytes.Buffer
	total := 0
	count := map[string]int{}
	for _, ph := range phases {
		for _, p := range ph.Problems {
			total++
			count[p.Diff]++
		}
	}
	head, _ := os.ReadFile("tools/dbtool/README.head.md")
	b.Write(head)
	fmt.Fprintf(&b, "\n## Problem index\n\n**%d problems** — 🟢 %d Easy · 🟡 %d Medium · 🔴 %d Hard\n\n",
		total, count["Easy"], count["Medium"], count["Hard"])
	for _, ph := range phases {
		fmt.Fprintf(&b, "- [Phase %d — %s](#phase-%d)\n", ph.Num, ph.Title, ph.Num)
	}
	for _, ph := range phases {
		fmt.Fprintf(&b, "\n<a id=\"phase-%d\"></a>\n\n### Phase %d — %s\n\n", ph.Num, ph.Num, ph.Title)
		if ph.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", ph.Description)
		}
		fmt.Fprintf(&b, "📁 [`%s/`](%s/) · 📖 [phase notes](%s/README.md) · ▶️ `make phase P=%02d`\n\n", ph.Dir, ph.Dir, ph.Dir, ph.Num)
		b.WriteString("| # | Problem | Level | What you build | Tests |\n|---|---|---|---|---|\n")
		for _, p := range ph.Problems {
			test := "—"
			if p.TestFile != "" {
				test = fmt.Sprintf("[%d test%s](%s#L%d)", p.Tests, plural(p.Tests), p.TestFile, p.TestLine)
			}
			fmt.Fprintf(&b, "| %s | [`%s`](%s#L%d) | %s | %s | %s |\n",
				p.ID(), p.Name, p.File, p.Line, badge(p.Diff), escape(p.Summary), test)
		}
	}
	return os.WriteFile("README.md", b.Bytes(), 0o644)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func badge(d string) string {
	switch d {
	case "Easy":
		return "🟢 Easy"
	case "Medium":
		return "🟡 Medium"
	}
	return "🔴 Hard"
}

func escape(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// ---------------------------------------------------------------- check

func check(phases []*phase) error {
	bad := 0
	for _, ph := range phases {
		if len(ph.Problems) == 0 {
			fmt.Printf("%s: no problems found\n", ph.Dir)
			bad++
		}
		seen := map[int]bool{}
		for i, p := range ph.Problems {
			if p.Phase != ph.Num {
				fmt.Printf("%s:%d: Problem %s is in phase dir %d\n", p.File, p.Line, p.ID(), ph.Num)
				bad++
			}
			if seen[p.Num] {
				fmt.Printf("%s:%d: duplicate Problem %s\n", p.File, p.Line, p.ID())
				bad++
			}
			seen[p.Num] = true
			if p.Num != i+1 {
				fmt.Printf("%s:%d: Problem %s breaks numbering (want %d.%d)\n", p.File, p.Line, p.ID(), ph.Num, i+1)
				bad++
			}
			if p.Tests == 0 {
				fmt.Printf("%s:%d: Problem %s has no %s* test\n", p.File, p.Line, p.ID(), p.TestPrefix())
				bad++
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d issue(s)", bad)
	}
	fmt.Println("check: ok")
	return nil
}

// ---------------------------------------------------------------- progress

func progress(phases []*phase) error {
	type res struct {
		p   *problem
		ok  bool
		msg string
	}
	var all []*problem
	for _, ph := range phases {
		all = append(all, ph.Problems...)
	}
	results := make([]res, len(all))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, p := range all {
		wg.Add(1)
		go func(i int, p *problem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cmd := exec.Command("go", "test", "-count=1", "-run", "^"+p.TestPrefix(), "./"+filepath.Dir(p.File)+"/")
			out, err := cmd.CombinedOutput()
			switch {
			case bytes.Contains(out, []byte("no tests to run")):
				results[i] = res{p, false, "no tests"}
			case err != nil && bytes.Contains(out, []byte("TODO: Problem")):
				results[i] = res{p, false, "todo"}
			case err != nil:
				results[i] = res{p, false, "fail"}
			default:
				results[i] = res{p, true, ""}
			}
		}(i, p)
	}
	wg.Wait()
	solved := 0
	cur := -1
	for _, r := range results {
		if r.p.Phase != cur {
			cur = r.p.Phase
			fmt.Printf("\nPhase %d\n", cur)
		}
		mark := "✅"
		switch {
		case r.ok:
			solved++
		case r.msg == "todo":
			mark = "⬜"
		default:
			mark = "❌"
		}
		fmt.Printf("  %s %-5s %-28s %s\n", mark, r.p.ID(), r.p.Name, r.p.Diff)
	}
	fmt.Printf("\nSolved %d / %d\n", solved, len(all))
	return nil
}

// ---------------------------------------------------------------- template

// template rewrites every problem func body to panic("TODO: Problem X.Y"),
// deletes decls whose doc contains "solution-only", and drops imports that
// become unused. Test files are left alone.
func template(phases []*phase) error {
	for _, ph := range phases {
		files, _ := filepath.Glob(filepath.Join(ph.Dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			if err := templateFile(f); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
	}
	return nil
}

func isSolutionOnly(doc *ast.CommentGroup) bool {
	return doc != nil && strings.Contains(doc.Text(), "solution-only")
}

func templateFile(path string) error {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	type span struct{ from, to token.Pos }
	var dropped []span
	var decls []ast.Decl
	changed := false
	for _, d := range af.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if isSolutionOnly(d.Doc) {
				dropped = append(dropped, span{d.Doc.Pos(), d.End()})
				changed = true
				continue
			}
			if p := parseMarker(d.Doc); p != nil && d.Body != nil {
				dropped = append(dropped, span{d.Body.Lbrace, d.Body.Rbrace})
				d.Body = &ast.BlockStmt{
					Lbrace: d.Body.Lbrace,
					List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{
						Fun:  ast.NewIdent("panic"),
						Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote("TODO: Problem " + p.ID())}},
					}}},
					Rbrace: d.Body.Rbrace,
				}
				changed = true
			}
		case *ast.GenDecl:
			if isSolutionOnly(d.Doc) {
				dropped = append(dropped, span{d.Doc.Pos(), d.End()})
				changed = true
				continue
			}
		}
		decls = append(decls, d)
	}
	if !changed {
		return nil
	}
	af.Decls = decls
	var keep []*ast.CommentGroup
	for _, cg := range af.Comments {
		inside := false
		for _, s := range dropped {
			if cg.Pos() >= s.from && cg.End() <= s.to {
				inside = true
				break
			}
		}
		if !inside {
			keep = append(keep, cg)
		}
	}
	af.Comments = keep
	removeUnusedImports(af)

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, af); err != nil {
		return err
	}
	out, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format: %w\n%s", err, buf.String())
	}
	out = todoGapRE.ReplaceAll(out, []byte("$1\n}"))
	return os.WriteFile(path, out, 0o644)
}

var todoGapRE = regexp.MustCompile(`(panic\("TODO: Problem [0-9.]+"\))\n\s*\n}`)

func removeUnusedImports(af *ast.File) {
	used := map[string]bool{}
	ast.Inspect(af, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	var decls []ast.Decl
	for _, d := range af.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			decls = append(decls, d)
			continue
		}
		var specs []ast.Spec
		for _, s := range gd.Specs {
			is := s.(*ast.ImportSpec)
			path, _ := strconv.Unquote(is.Path.Value)
			name := filepath.Base(path)
			if is.Name != nil {
				name = is.Name.Name
			}
			if name == "_" || name == "." || used[name] {
				specs = append(specs, s)
			}
		}
		if len(specs) == 0 {
			continue
		}
		gd.Specs = specs
		decls = append(decls, gd)
	}
	af.Decls = decls
	var imps []*ast.ImportSpec
	for _, d := range af.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			for _, s := range gd.Specs {
				imps = append(imps, s.(*ast.ImportSpec))
			}
		}
	}
	af.Imports = imps
}

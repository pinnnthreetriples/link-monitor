package gates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syncSources are the files the shared folder is made of: the decision, the
// two filesystems, the loop, the API and the wiring. Tests are left out — a
// test that stands up a temporary tree may of course clean it up.
var syncSources = []string{
	"internal/core/foldersync",
	"internal/adapters/syncfs",
	"internal/app/foldersync.go",
	"internal/app/foldersyncpass.go",
	"internal/app/foldersynctext.go",
	"internal/app/httpapi/sync.go",
	"cmd/linkmon/foldersync.go",
}

// destructive are the calls that can lose a byte somebody wanted. Removal is
// the obvious one; O_TRUNC is here because truncating a destination in place
// is the other way to write half a file, and rule 3 says the destination is
// only ever reached by a rename.
var destructive = map[string]bool{
	"Remove":          true,
	"RemoveAll":       true,
	"RemoveDirectory": true,
	"Unlink":          true,
	"Truncate":        true,
	"O_TRUNC":         true,
}

// mayRemove are the only two functions in the whole feature allowed to contain
// one of those calls, and both of them are handed the name of a temporary file
// this program created moments earlier and can still see.
//
// The allowlist is by function rather than by comment on purpose: a marker a
// saboteur can copy onto their own line is not a guard, and "which function is
// this written inside" is a question they cannot answer their way out of
// without saying so here, in front of this comment.
var mayRemove = map[string]bool{
	"localPublisher.remove":  true,
	"remotePublisher.remove": true,
}

// TestNothingInTheSharedFolderCanDeleteAFile is the other half of rule 1.
//
// internal/core/foldersync's own guards prove that no *plan* can ask for a
// file to be removed. They cannot see what the code carrying a plan out does,
// and a deletion there would be exactly as destructive and rather easier to
// write by accident — a "cleanup" of the destination before a copy, a
// "tidy-up" of a stale file, an os.RemoveAll where an os.Remove was meant.
// This reads the source and answers that question directly.
//
// It lives in tools/gates for the same reason the size rule does: it is a rule
// about the repository, and no package in it can assert this about another.
func TestNothingInTheSharedFolderCanDeleteAFile(t *testing.T) {
	root := repoRoot(t)

	var checked int
	for _, target := range syncSources {
		for _, path := range goFilesUnder(t, filepath.Join(root, target)) {
			checked++
			checkNoDeletes(t, root, path)
		}
	}
	// A gate that silently policed nothing would be worse than no gate: it
	// would read as a guarantee in the test output.
	if checked < len(syncSources) {
		t.Fatalf("only %d files were checked for %d targets — the list in "+
			"syncSources has gone stale", checked, len(syncSources))
	}
}

// goFilesUnder lists the non-test Go files at a path, which may be one file or
// a directory.
func goFilesUnder(t *testing.T, path string) []string {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("looking at %s: %v — syncSources names a file that is not there", path, err)
	}
	if !info.IsDir() {
		return []string{path}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(path, name))
	}
	return out
}

// checkNoDeletes fails for every destructive call outside the two functions
// allowed to make one.
func checkNoDeletes(t *testing.T, root, path string) {
	t.Helper()

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}

	for _, decl := range parsed.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc {
			continue
		}
		name := funcName(fn)
		ast.Inspect(fn, func(node ast.Node) bool {
			word, found := destructiveWord(node)
			if !found || mayRemove[name] {
				return true
			}
			t.Errorf("%s:%d: %s calls %s — nothing in the shared folder may delete or "+
				"truncate a file. If a temporary file of our own really has to go, it goes "+
				"through the two functions named in mayRemove, and this comment is the "+
				"argument you have to answer first.",
				rel, fset.Position(node.Pos()).Line, name, word)
			return true
		})
	}
}

// destructiveWord reports the destructive identifier a node names, if any.
func destructiveWord(node ast.Node) (string, bool) {
	sel, isSelector := node.(*ast.SelectorExpr)
	if !isSelector {
		return "", false
	}
	if destructive[sel.Sel.Name] {
		return sel.Sel.Name, true
	}
	return "", false
}

// funcName is "Type.Method" for a method and "Func" for a function, which is
// the form mayRemove is written in.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

// receiverName is the bare type name of a receiver, pointer or not.
func receiverName(expr ast.Expr) string {
	if star, isPointer := expr.(*ast.StarExpr); isPointer {
		expr = star.X
	}
	if ident, isIdent := expr.(*ast.Ident); isIdent {
		return ident.Name
	}
	return "?"
}

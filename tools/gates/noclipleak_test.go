package gates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// clipSources are the files the shared clipboard is made of: the decision, the
// two Win32 halves, the delivery to the peer, the loop, the API and the
// wiring. Tests are left out — a test may of course print what it is asserting
// about, and it says so in the test output and nowhere else.
var clipSources = []string{
	"internal/core/clipshare",
	"internal/adapters/clipboard",
	"internal/adapters/peerclip",
	"internal/app/clipshare.go",
	"internal/app/clipsharepass.go",
	"internal/app/clipsharetext.go",
	"internal/app/httpapi/clip.go",
	"cmd/linkmon/clipshare.go",
}

// sinkImports are the packages that can put a byte somewhere it outlives the
// process, or somewhere a person can read it later. Rule 4 says the clipboard's
// content is never logged and never written to disk — not the content, not a
// prefix, not a length and a hash of it, not "for debugging" — and the cheapest
// way to be sure of that is for the code that handles it to have nothing to
// write with.
//
// os is here for the filesystem, not for os.Getenv: nothing in this feature
// needs either, and one rule with no exceptions is worth more than two with.
var sinkImports = map[string]string{
	"log":                               "the clipboard's content may never reach a log",
	"log/slog":                          "the clipboard's content may never reach a log",
	"os":                                "nothing in the shared clipboard writes to disk",
	"io/ioutil":                         "nothing in the shared clipboard writes to disk",
	"os/exec":                           "a child process is one more place content could go",
	"golang.org/x/sys/windows/registry": "the registry outlives the process; nothing here may write to it",
}

// sinkCalls are the calls that print or persist, whatever they were imported
// as. The import rule above is the fence; this is the check that nobody walked
// round it with an http.ResponseWriter, a bytes.Buffer handed in from
// somewhere, or a logger reached through an interface.
//
// Error is in the list twice over: it is slog's method for writing a line, and
// it is also err.Error(), which turns an error into a string for something to
// then print — and an error built inside this feature is the one place a
// careless "%s" of the content would hide.
var sinkCalls = map[string]string{
	"Print": "prints", "Printf": "prints", "Println": "prints",
	"Fprint": "prints", "Fprintf": "prints", "Fprintln": "prints",
	"Debug": "logs", "Info": "logs", "Warn": "logs", "Error": "logs or stringifies an error",
	"DebugContext": "logs", "InfoContext": "logs", "WarnContext": "logs", "ErrorContext": "logs",
	"Log": "logs", "LogAttrs": "logs", "SetOutput": "redirects a log",
	"Fatal": "logs and exits", "Fatalf": "logs and exits", "Fatalln": "logs and exits",
	"WriteFile": "writes a file", "Create": "creates a file", "CreateTemp": "creates a file",
	"OpenFile": "opens a file for writing",
	"Write":    "writes bytes somewhere", "WriteString": "writes bytes somewhere",
}

// mayCarryText is every function in the world that a piece of clipboard
// content may be handed to, by the name written at the call site.
//
// This is the rule with teeth. Rule 4 is not only about logs and files: content
// handed to any function this gate cannot see could be written down by that
// function, and there is no way to check what a name like Save or Report does.
// So the list is closed. Adding a call that takes the content means adding its
// name here, in front of this comment, where the question "and where does that
// put it?" has to be answered out loud.
//
// The entries are, in order: the builtins and conversions that cannot leak
// anything by themselves; this feature's own vocabulary — fingerprinting,
// zeroing, deciding, encoding for the one wire it has, and the two halves of
// the UTF-16 conversion; the three seams the content legitimately crosses (the
// clipboard, the peer's instance, and the loop's own Receive); and the standard
// library calls those are built out of.
var mayCarryText = map[string]string{
	"len":    "measures, and cannot keep",
	"append": "copies into a buffer this feature owns and zeroes",
	"copy":   "as append",
	"string": "a conversion; the result is content and is policed as content",
	"[]byte": "a conversion, the other way",
	"min":    "arithmetic",

	"Fingerprint": "clipshare.Fingerprint: a hash held in memory for one comparison",
	"Zero":        "clipshare.Zero: destroys the buffer, which is the opposite of a leak",
	"Decide":      "clipshare.Decide: pure, returns a token and a hash",
	"Sum256":      "crypto/sha256, inside Fingerprint",

	"Put":     "app.Clipboard.Put: this machine's own clipboard, which is where it is going",
	"Deliver": "app.PeerInbox.Deliver: the peer's instance, over SSH inside WireGuard",
	"Receive": "app.Clip.Receive: the loop's own entry point for an arriving item",
	"Look":    "app.Clipboard.Look: takes a size, never content — listed for the cap argument",

	"post":       "peerclip.post: builds the one request this feature makes and sends it",
	"encodeItem": "peerclip.encodeItem: the one JSON body this feature sends",
	"Marshal":    "encoding/json, inside encodeItem",
	"NewReader":  "bytes.NewReader, wrapping that body for one request",

	"plant": "app.Clip.plant: takes the fingerprint, which is the echo defence, and keeps it in memory",

	"toUTF16":    "the clipboard adapter's own conversion into the buffer Windows takes",
	"toUTF8":     "the same, coming back",
	"copyInto":   "RtlMoveMemory into the block handed to the clipboard",
	"copyOut":    "RtlMoveMemory out of the block the clipboard holds",
	"DecodeRune": "unicode/utf8, inside the conversion",
	"AppendRune": "unicode/utf16 and unicode/utf8, inside the conversions",
	"decodeRune": "the adapter's own surrogate-pair reader",
	"oversized":  "the adapter's own refusal, which zeroes what it was given",
	"zero16":     "as clipshare.Zero, for UTF-16",
}

// TestNothingInTheSharedClipboardCanLeakWhatWasCopied is rule 4, asserted
// about the source rather than hoped for.
//
// Every other rule of this feature is visible in its behaviour and is tested
// where it is decided: internal/core/clipshare proves that a marked item and an
// oversized one go nowhere, and internal/app proves that an arriving item does
// not bounce back. Rule 4 is different. A log line is not behaviour anybody
// notices, a leak of it would be invisible in every test, and the tempting
// version of it — "just the length, just a hash, just while I debug this" — is
// one line a person adds in a hurry and nobody reviews. So it is checked here,
// in three parts:
//
//   - the packages that can write a byte somewhere lasting are not imported,
//     so there is nothing in scope to log or persist with;
//   - the calls that print or write are absent whatever they were reached
//     through, so an io.Writer or a logger handed in from elsewhere is no way
//     round the first part;
//   - and a piece of content may only be passed to a function named in
//     [mayCarryText], so it cannot be handed to code this gate cannot see.
//
// It lives in tools/gates for the same reason the size rule and the shared
// folder's deletion rule do: it is a rule about the repository, and no package
// in it can assert this about another.
func TestNothingInTheSharedClipboardCanLeakWhatWasCopied(t *testing.T) {
	root := repoRoot(t)

	var checked int
	for _, target := range clipSources {
		for _, path := range goFilesUnder(t, filepath.Join(root, target)) {
			checked++
			checkNoClipLeak(t, root, path)
		}
	}
	// A gate that silently policed nothing would be worse than no gate: it
	// would read as a guarantee in the test output.
	if checked < len(clipSources) {
		t.Fatalf("only %d files were checked for %d targets — the list in "+
			"clipSources has gone stale", checked, len(clipSources))
	}
}

// checkNoClipLeak applies all three parts to one file.
func checkNoClipLeak(t *testing.T, root, path string) {
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
	where := func(node ast.Node) string {
		return rel + ":" + strconv.Itoa(fset.Position(node.Pos()).Line)
	}

	checkImports(t, parsed, where)
	for _, decl := range parsed.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Body == nil {
			continue
		}
		checkCalls(t, funcName(fn), fn.Body, where)
	}
}

// checkImports is the first part: nothing in scope to write with.
func checkImports(t *testing.T, file *ast.File, where func(ast.Node) string) {
	t.Helper()

	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if why, forbidden := sinkImports[path]; forbidden {
			t.Errorf("%s: the shared clipboard imports %q — %s. Rule 4 has no "+
				"exception for a length, a hash or a debugging line, and the comment "+
				"on sinkImports is the argument to answer first.",
				where(spec), path, why)
		}
	}
}

// checkCalls is the second and third parts, over one function body.
func checkCalls(t *testing.T, fn string, body *ast.BlockStmt, where func(ast.Node) string) {
	t.Helper()

	ast.Inspect(body, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		name := calleeName(call.Fun)
		if why, isSink := sinkCalls[name]; isSink {
			t.Errorf("%s: %s calls %s, which %s — nothing in the shared clipboard "+
				"prints or persists anything. See the comment on sinkCalls.",
				where(call), fn, name, why)
		}
		if !carriesText(call.Args) {
			return true
		}
		if _, allowed := mayCarryText[name]; !allowed {
			t.Errorf("%s: %s hands clipboard content to %s, which is not in "+
				"mayCarryText — content may only go to a function this gate has been "+
				"told about. Add it there, with what it does with the bytes, and the "+
				"comment on mayCarryText is the argument you have to answer first.",
				where(call), fn, name)
		}
		return true
	})
}

// carriesText reports whether any of these arguments mentions clipboard
// content anywhere inside it.
//
// len(text) is the one thing it deliberately does not count. A size is not
// content: the window shows sizes, the API carries them, and an argument that
// says how many bytes there were says nothing about what they were. Everything
// else inside an argument counts, including a slice of the content, a
// conversion of it and a struct literal holding it.
func carriesText(args []ast.Expr) bool {
	for _, arg := range args {
		found := false
		ast.Inspect(arg, func(node ast.Node) bool {
			if call, isCall := node.(*ast.CallExpr); isCall && calleeName(call.Fun) == "len" {
				return false
			}
			if isTextExpr(node) {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// isTextExpr is the convention this feature is written to: clipboard content
// lives in something called text, or in a field called Text, and nowhere else.
//
// A convention is a weak thing to hang a rule on, which is why it is not the
// only rule here — the two absolute ones above stand whatever anything is
// called. What it buys is the third: a name a reader can follow, and a gate
// that notices when the bytes behind that name are handed to something new.
func isTextExpr(node ast.Node) bool {
	switch expr := node.(type) {
	case *ast.Ident:
		return expr.Name == "text"
	case *ast.SelectorExpr:
		return expr.Sel.Name == "Text"
	default:
		return false
	}
}

// calleeName is the name written at a call site: "Printf" for fmt.Printf and
// for log.Printf alike, "Put" for c.deps.Here.Put, "[]byte" for a conversion.
//
// The last selector rather than the whole expression, on purpose. A rule keyed
// to "fmt.Printf" would be walked round by aliasing the import; a rule keyed to
// the name a reader sees cannot be, and the cost is that an unrelated method
// sharing one of these names has to say so in the list.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.ArrayType:
		if ident, isIdent := f.Elt.(*ast.Ident); isIdent {
			return "[]" + ident.Name
		}
		return "[]?"
	case *ast.ParenExpr:
		return calleeName(f.X)
	case *ast.IndexExpr:
		return calleeName(f.X)
	case *ast.StarExpr:
		return calleeName(f.X)
	default:
		return strings.TrimSpace("?")
	}
}

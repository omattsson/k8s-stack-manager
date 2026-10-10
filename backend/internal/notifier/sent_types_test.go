package notifier

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notifySend describes a function that sends a notification: the index of
// its notifType argument and its number of arguments. The number of
// arguments tells it apart from other functions with the same name (for
// example signal.Notify). The notifier methods and the deployer wrapper
// (Manager.notifyInstance) are listed; calls inside the notifier package are
// not scanned.
type notifySend struct {
	typeArg int
	args    int
}

var notifyTypeArg = map[string]notifySend{
	"Notify":                   {typeArg: 2, args: 7}, // ctx, userID, notifType, title, message, entityType, entityID
	"NotifyInstance":           {typeArg: 2, args: 5}, // ctx, target, notifType, title, message
	"NotifySystem":             {typeArg: 1, args: 6}, // ctx, notifType, title, message, entityType, entityID
	"NotifySystemForInstances": {typeArg: 1, args: 7}, // ctx, notifType, title, message, entityType, entityID, instances
	"notifyInstance":           {typeArg: 1, args: 4}, // target, notifType, title, message
}

// TestSentEventTypesAreKnown scans the backend source for every call that
// sends a notification and checks that each event type it can send is in
// PreferenceEventTypes (issue #498). The type argument must be a string
// literal, or a local variable that gets only string literals in the same
// function, or a parameter of a function that is itself in notifyTypeArg.
// A new send with another form fails here: use a literal.
func TestSentEventTypesAreKnown(t *testing.T) {
	t.Parallel()

	known := map[string]bool{}
	for _, et := range PreferenceEventTypes() {
		known[et] = true
	}

	// The internal packages (without this package) and the api bootstrap.
	internalRoot := filepath.Join("..")
	roots := []string{internalRoot, filepath.Join("..", "..", "api")}
	fset := token.NewFileSet()
	sent := map[string][]string{} // type -> positions
	sites := 0

	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Base(path) == "notifier" && filepath.Dir(path) == internalRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					callee = fun.Sel.Name
				case *ast.Ident:
					callee = fun.Name
				default:
					return true
				}
				send, ok := notifyTypeArg[callee]
				if !ok || len(call.Args) != send.args {
					return true
				}
				idx := send.typeArg
				pos := fset.Position(call.Pos()).String()
				types, resolved := sentTypes(fn, call.Args[idx])
				if !resolved {
					t.Errorf("%s: cannot resolve the notification type argument; use a string literal", pos)
					return true
				}
				sites++
				for _, et := range types {
					sent[et] = append(sent[et], pos)
				}
				return true
			})
		}
		return nil
	}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, walk))
	}

	require.Greater(t, sites, 20, "expected to find the notification send sites")
	for et, positions := range sent {
		assert.Truef(t, known[et], "event type %q is sent (%s) but missing from PreferenceEventTypes", et, strings.Join(positions, ", "))
	}
	// Every listed type is sent somewhere (no stale entries).
	for et := range known {
		assert.Containsf(t, sent, et, "event type %q is listed but never sent", et)
	}
}

// sentTypes returns the string values that arg can have in fn. A parameter
// of a send wrapper gives no values (its callers are checked). It returns
// false when arg has another form.
func sentTypes(fn *ast.FuncDecl, arg ast.Expr) ([]string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return nil, false
		}
		v, err := strconv.Unquote(a.Value)
		return []string{v}, err == nil
	case *ast.Ident:
		if isParam(fn, a.Name) {
			_, wrapper := notifyTypeArg[fn.Name.Name]
			return nil, wrapper
		}
		return literalAssignments(fn, a.Name)
	}
	return nil, false
}

// isParam reports whether name is a parameter of fn.
func isParam(fn *ast.FuncDecl, name string) bool {
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// literalAssignments returns the values of all assignments to the local
// variable name in fn. It returns false when an assignment is not a string
// literal or when there is no assignment.
func literalAssignments(fn *ast.FuncDecl, name string) ([]string, bool) {
	var values []string
	ok := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, isAssign := n.(*ast.AssignStmt)
		if !isAssign {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, isIdent := lhs.(*ast.Ident)
			if !isIdent || id.Name != name {
				continue
			}
			if len(assign.Rhs) != len(assign.Lhs) {
				ok = false
				continue
			}
			lit, isLit := assign.Rhs[i].(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				ok = false
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				ok = false
				continue
			}
			values = append(values, v)
		}
		return true
	})
	return values, ok && len(values) > 0
}

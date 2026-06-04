package secfile_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestConversationFilesDoNotWriteWorldReadable walks every
// internal/<provider>/conversation.go file and fails the build if any
// of them call os.WriteFile / os.MkdirAll with a loose Unix mode
// literal, or call os.ReadFile (which bypasses the chmod-on-load
// repair). The intent is to lock in #22 so a tenth provider can't
// reintroduce the leak by copy-pasting the historic pattern.
func TestConversationFilesDoNotWriteWorldReadable(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	var files []string
	if err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) == "conversation.go" {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(files) < 9 {
		t.Fatalf("expected >= 9 conversation.go files (one per provider); found %d. Did the layout change?", len(files))
	}

	fset := token.NewFileSet()
	for _, path := range files {
		t.Run(relpath(root, path), func(t *testing.T) {
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "os" {
					return true
				}
				switch sel.Sel.Name {
				case "WriteFile", "ReadFile":
					t.Errorf("%s uses os.%s directly — must go through secfile.WritePrivate/ReadPrivate (drift guard for #22)", fset.Position(call.Pos()), sel.Sel.Name)
				case "MkdirAll":
					t.Errorf("%s uses os.MkdirAll directly — must go through secfile.EnsurePrivateDir (drift guard for #22)", fset.Position(call.Pos()))
				}
				return true
			})
		})
	}
}

func repoRoot() (string, error) {
	// internal/secfile/drift_test.go → ../..
	abs, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(abs, "..", "..")), nil
}

func relpath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}

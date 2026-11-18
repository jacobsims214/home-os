package main

// #4503: the registry is assembled in register.go, one Register function per
// internal package. A package whose Register is written but never added to
// registrars still compiles, vets and passes every other test; the only
// symptom is that its tools silently never reach tools/list. These tests close
// that hole from the packaging side: every internal package that exports
// Register must appear in registrars, exactly once, and the set registrar
// actually serves must equal the catalog (wantTools).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
)

// internalPackagesWithRegister returns the import path of every package under
// internal/ that exports a func Register with two parameters, the shape
// registrars holds.
func internalPackagesWithRegister(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "internal")
	fset := token.NewFileSet()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "Register" {
				continue
			}
			if fn.Type.Params == nil || len(fn.Type.Params.List) != 2 {
				continue
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			pkg := "home-os/mcp/internal/" + filepath.ToSlash(rel)
			if !slices.Contains(out, pkg) {
				out = append(out, pkg)
			}
			return nil
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// A floor, so a broken walk can never make this test pass vacuously.
	if len(out) < 20 {
		t.Fatalf("found only %d packages with Register under %s; the AST walk is broken", len(out), root)
	}
	return out
}

// registrarPackageName returns the internal package an entry of registrars
// belongs to, from the function value's own name
// ("home-os/mcp/internal/bill.Register" -> "home-os/mcp/internal/bill").
func registrarPackageName(fn func(*server.Server, server.Deps)) string {
	f := runtime.FuncForPC(reflect.ValueOf(fn).Pointer())
	if f == nil {
		return ""
	}
	return strings.TrimSuffix(f.Name(), ".Register")
}

// TestEveryRegisterFuncIsCalled fails when an internal package exports
// Register but registrars (cmd/mcp/register.go) never calls it, which would
// leave that package's tools off tools/list with nothing else failing.
func TestEveryRegisterFuncIsCalled(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	for _, fn := range registrars {
		name := registrarPackageName(fn)
		if name == "" {
			t.Fatalf("cannot resolve the package of a registrars entry; the reflect-based lookup is broken")
		}
		registered[name] = true
	}

	for _, pkg := range internalPackagesWithRegister(t) {
		if !registered[pkg] {
			t.Errorf("%s exports Register but registrars never calls it, so its tools never reach tools/list. Add %s.Register to registrars in cmd/mcp/register.go.",
				pkg, filepath.Base(pkg))
		}
	}
}

// TestRegistrarsListedOnce fails on a package listed twice: mcp-go panics on
// a duplicate tool name, so the server would not start.
func TestRegistrarsListedOnce(t *testing.T) {
	t.Parallel()

	seen := map[string]int{}
	for _, fn := range registrars {
		seen[registrarPackageName(fn)]++
	}
	for pkg, n := range seen {
		if n > 1 {
			t.Errorf("%s.Register is in registrars %d times; the second call panics on a duplicate tool name", pkg, n)
		}
	}
}

// TestRegisterAllServesTheCatalog registers everything exactly as main does
// and proves the served tool set is the catalog: same names as wantTools, and
// a tool count that leaves no room for an extra unlisted tool.
func TestRegisterAllServesTheCatalog(t *testing.T) {
	t.Parallel()

	srv := server.NewServer(&config.Config{}, nil, nil)
	registerAll(srv, &config.Config{}, nil)

	served := srv.MCPServer().ListTools()
	if len(served) != len(wantTools) {
		t.Errorf("tools/list serves %d tools, wantTools lists %d: names served but not in wantTools: %v",
			len(served), len(wantTools), missingFrom(served, wantTools))
	}
	if srv.ToolCount() != len(wantTools) {
		t.Errorf("ToolCount = %d, wantTools lists %d (registerAll must not register anything twice)",
			srv.ToolCount(), len(wantTools))
	}
	for _, name := range wantTools {
		if _, ok := served[name]; !ok {
			t.Errorf("wantTools lists %q but tools/list does not serve it: the package registering it is not in registrars", name)
		}
	}
}

// missingFrom returns the names served but not expected, sorted for a stable
// failure message.
func missingFrom[T any](served map[string]T, want []string) []string {
	var out []string
	for name := range served {
		if !slices.Contains(want, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

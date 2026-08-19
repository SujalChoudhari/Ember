package postgres_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIntegrationStoreAtURLIsolationContract(t *testing.T) {
	_, helperPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration helper source")
	}
	helperSourcePath := filepath.Join(filepath.Dir(helperPath), "integration_helpers_test.go")
	source, err := os.ReadFile(helperSourcePath)
	if err != nil {
		t.Fatalf("read integration helper source: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), helperSourcePath, source, 0)
	if err != nil {
		t.Fatalf("parse integration helper source: %v", err)
	}
	var helperBody *ast.BlockStmt
	ast.Inspect(file, func(node ast.Node) bool {
		function, ok := node.(*ast.FuncDecl)
		if ok && function.Name.Name == "integrationStoreAtURL" {
			helperBody = function.Body
			return false
		}
		return true
	})
	if helperBody == nil {
		t.Fatal("integrationStoreAtURL helper is missing")
	}
	callsIsolationHelper := false
	forbiddenMutation := false
	ast.Inspect(helperBody, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.Ident); ok && selector.Name == "isolatedDatabaseURLAtBase" {
			callsIsolationHelper = true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "ExecContext" {
			forbiddenMutation = true
		}
		return true
	})
	if !callsIsolationHelper {
		t.Fatal("integrationStoreAtURL must create a disposable isolation boundary before opening a store")
	}
	if forbiddenMutation || strings.Contains(string(source[helperBody.Pos()-1:helperBody.End()-1]), "TRUNCATE") {
		t.Fatal("integrationStoreAtURL must not execute cleanup SQL against its caller-supplied URL")
	}
}

package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// Task creation is a non-idempotent POST. Keep a source-level guard that
// RelayTaskSubmit cannot be placed back inside a retry loop by accident.
func TestRelayTaskSubmitIsNotNestedInRetryLoop(t *testing.T) {
	source, err := os.ReadFile("relay.go")
	if err != nil {
		t.Fatalf("read relay.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "relay.go", source, 0)
	if err != nil {
		t.Fatalf("parse relay.go: %v", err)
	}

	var relayTask *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "RelayTask" {
			relayTask = fn
			break
		}
	}
	if relayTask == nil || relayTask.Body == nil {
		t.Fatal("RelayTask function not found")
	}

	loops := make(map[ast.Node]bool)
	ast.Inspect(relayTask.Body, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			loops[node] = true
		}
		return true
	})

	ast.Inspect(relayTask.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RelayTaskSubmit" {
			return true
		}

		for loop := range loops {
			if call.Pos() >= loop.Pos() && call.End() <= loop.End() {
				t.Errorf("RelayTaskSubmit is nested in a retry loop at %s", fset.Position(call.Pos()))
			}
		}
		return true
	})
}

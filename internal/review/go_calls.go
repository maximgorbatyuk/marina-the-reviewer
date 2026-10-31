package review

import (
	"context"
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

type goTypeReference struct {
	file        *goSourceFile
	name        string
	pointer     bool
	addressable bool
}

type goScope struct {
	bindings map[string]goTypeReference
	parent   *goScope
}

func (scope goScope) child() goScope {
	return goScope{bindings: map[string]goTypeReference{}, parent: &scope}
}

func (scope goScope) lookup(name string) (goTypeReference, bool) {
	for current := &scope; current != nil; current = current.parent {
		if typ, found := current.bindings[name]; found {
			return typ, true
		}
	}
	return goTypeReference{}, false
}

type goCallWalk struct {
	context     context.Context
	analyzer    *goSourceAnalyzer
	declaration *goDeclaration
	graphIDs    map[string]string
	edges       []GraphEdge
	references  map[*goDeclaration]bool
	err         error
	typeDepth   int
	typeSteps   int
}

func (analyzer *goSourceAnalyzer) callGraph(ctx context.Context, graphIDs map[string]string) ([]GraphEdge, map[*goDeclaration][]*goDeclaration, error) {
	edges := []GraphEdge{}
	references := map[*goDeclaration][]*goDeclaration{}
	for _, file := range analyzer.ordered {
		for _, declaration := range file.declarations {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			walker := &goCallWalk{context: ctx, analyzer: analyzer, declaration: declaration, graphIDs: graphIDs, references: map[*goDeclaration]bool{}}
			scope := goScope{bindings: map[string]goTypeReference{}}
			if function, ok := declaration.node.(*ast.FuncDecl); ok {
				walker.bindFields(scope, function.Recv)
				walker.bindFields(scope, function.Type.Params)
				walker.bindFields(scope, function.Type.Results)
				walker.bindFields(scope, function.Type.TypeParams)
			}
			walker.walk(declaration.node, scope)
			if walker.err != nil {
				return nil, nil, walker.err
			}
			edges = append(edges, walker.edges...)
			for reference := range walker.references {
				references[declaration] = append(references[declaration], reference)
			}
			slices.SortStableFunc(references[declaration], func(a, b *goDeclaration) int {
				if a.file.source.Path != b.file.source.Path {
					return ordinalCompare(a.file.source.Path, b.file.source.Path)
				}
				return a.source.StartLine - b.source.StartLine
			})
		}
	}
	return edges, references, ctx.Err()
}

func (walker *goCallWalk) bindFields(scope goScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		typ := walker.typeReference(field.Type)
		typ.addressable = true
		for _, name := range field.Names {
			scope.bindings[name.Name] = typ
		}
	}
}

func (walker *goCallWalk) typeReference(expression ast.Expr) goTypeReference {
	file := walker.declaration.file
	result := goTypeReference{file: file}
	for {
		switch node := expression.(type) {
		case *ast.StarExpr:
			result.pointer = true
			expression = node.X
		case *ast.IndexExpr:
			expression = node.X
		case *ast.IndexListExpr:
			expression = node.X
		case *ast.ParenExpr:
			expression = node.X
		case *ast.Ident:
			result.name = node.Name
			return result
		case *ast.SelectorExpr:
			name, ok := node.X.(*ast.Ident)
			if !ok {
				return goTypeReference{}
			}
			candidates := walker.analyzer.importFiles(file, file.imports[name.Name])
			if len(candidates) == 0 {
				return goTypeReference{}
			}
			result.file, result.name = candidates[0], node.Sel.Name
			return result
		default:
			return goTypeReference{}
		}
	}
}

func (walker *goCallWalk) expressionType(expression ast.Expr, scope goScope) goTypeReference {
	if walker.typeDepth >= 32 || walker.typeSteps >= 4096 {
		return goTypeReference{}
	}
	walker.typeSteps++
	if walker.err = walker.context.Err(); walker.err != nil {
		return goTypeReference{}
	}
	walker.typeDepth++
	defer func() { walker.typeDepth-- }()
	switch node := expression.(type) {
	case *ast.Ident:
		if typ, found := scope.lookup(node.Name); found {
			return typ
		}
		declarations := walker.compatible(walker.analyzer.packageDeclarations(walker.declaration.file, node.Name))
		if len(declarations) != 1 {
			return goTypeReference{}
		}
		for _, declaration := range declarations {
			if general, ok := declaration.node.(*ast.GenDecl); ok {
				for _, spec := range general.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for index, name := range value.Names {
							if name.Name != node.Name {
								continue
							}
							if value.Type != nil {
								typ := walker.typeReference(value.Type)
								typ.addressable = true
								return typ
							}
							if index < len(value.Values) {
								return walker.expressionType(value.Values[index], scope)
							}
						}
					}
				}
			}
		}
	case *ast.CompositeLit:
		return walker.typeReference(node.Type)
	case *ast.UnaryExpr:
		if node.Op == token.AND {
			typ := walker.expressionType(node.X, scope)
			typ.pointer = true
			return typ
		}
	case *ast.StarExpr:
		typ := walker.expressionType(node.X, scope)
		typ.pointer, typ.addressable = false, true
		return typ
	case *ast.ParenExpr:
		return walker.expressionType(node.X, scope)
	case *ast.CallExpr:
		if name, ok := node.Fun.(*ast.Ident); ok && name.Name == "new" && len(node.Args) == 1 {
			_, shadowed := scope.lookup("new")
			if shadowed || len(walker.analyzer.packageDeclarations(walker.declaration.file, "new")) != 0 {
				return goTypeReference{}
			}
			typ := walker.typeReference(node.Args[0])
			typ.pointer = true
			return typ
		}
		targets, _, _ := walker.resolveCall(node.Fun, scope)
		if len(targets) == 1 && targets[0].resultType != nil {
			other := *walker
			other.declaration = targets[0]
			return other.typeReference(targets[0].resultType)
		}
	case *ast.SelectorExpr:
		typ := walker.expressionType(node.X, scope)
		if typ.file == nil {
			return goTypeReference{}
		}
		declarations := walker.compatible(walker.analyzer.packageDeclarations(typ.file, typ.name))
		if len(declarations) != 1 {
			return goTypeReference{}
		}
		for _, declaration := range declarations {
			general, ok := declaration.node.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range general.Specs {
				spec, ok := specification.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := spec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						if name.Name == node.Sel.Name {
							other := *walker
							other.declaration = declaration
							result := other.typeReference(field.Type)
							result.addressable = typ.addressable || typ.pointer
							return result
						}
					}
				}
			}
		}
	}
	return goTypeReference{}
}

func (walker *goCallWalk) compatible(declarations []*goDeclaration) []*goDeclaration {
	result := []*goDeclaration{}
	file := walker.declaration.file
	for _, declaration := range declarations {
		if file.platform != "" && declaration.file.platform != "" && file.platform != declaration.file.platform || file.architecture != "" && declaration.file.architecture != "" && file.architecture != declaration.file.architecture {
			continue
		}
		if !strings.HasSuffix(file.source.Path, "_test.go") && strings.HasSuffix(declaration.file.source.Path, "_test.go") {
			continue
		}
		result = append(result, declaration)
	}
	return result
}

func (walker *goCallWalk) resolveCall(expression ast.Expr, scope goScope) ([]*goDeclaration, string, bool) {
	file := walker.declaration.file
	switch node := expression.(type) {
	case *ast.ParenExpr:
		return walker.resolveCall(node.X, scope)
	case *ast.IndexExpr:
		return walker.resolveCall(node.X, scope)
	case *ast.IndexListExpr:
		return walker.resolveCall(node.X, scope)
	case *ast.Ident:
		if _, shadowed := scope.lookup(node.Name); shadowed {
			return nil, node.Name, false
		}
		localTargets := walker.analyzer.packageDeclarations(file, node.Name)
		if len(localTargets) == 0 && slices.Contains([]string{"append", "cap", "clear", "close", "complex", "copy", "delete", "imag", "len", "make", "max", "min", "new", "panic", "print", "println", "real", "recover", "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr"}, node.Name) {
			return nil, node.Name, true
		}
		targets := localTargets
		for _, importPath := range file.dotImports {
			if ast.IsExported(node.Name) {
				for _, candidate := range walker.analyzer.importFiles(file, importPath) {
					targets = appendUniqueGoDeclarations(targets, walker.analyzer.packageDeclarations(candidate, node.Name)...)
				}
			}
		}
		return walker.callableTargets(targets, node.Name)
	case *ast.SelectorExpr:
		if alias, ok := node.X.(*ast.Ident); ok {
			if _, shadowed := scope.lookup(alias.Name); !shadowed {
				if importPath, imported := file.imports[alias.Name]; imported {
					targets := []*goDeclaration{}
					if ast.IsExported(node.Sel.Name) {
						for _, candidate := range walker.analyzer.importFiles(file, importPath) {
							targets = appendUniqueGoDeclarations(targets, walker.analyzer.packageDeclarations(candidate, node.Sel.Name)...)
						}
					}
					return walker.callableTargets(targets, node.Sel.Name)
				}
			}
		}
		typ := walker.expressionType(node.X, scope)
		if typ.file == nil || typ.name == "" {
			// An unbound type name can be a method expression, but not a value receiver.
			typ = walker.typeReference(node.X)
			if alias, ok := node.X.(*ast.Ident); ok {
				if _, shadowed := scope.lookup(alias.Name); shadowed {
					return nil, node.Sel.Name, false
				}
			}
		}
		if typ.file == nil || typ.name == "" {
			return nil, node.Sel.Name, false
		}
		targets := []*goDeclaration{}
		for _, method := range walker.methodTargets(typ, node.Sel.Name, map[string]bool{}) {
			pointer := false
			if function, ok := method.node.(*ast.FuncDecl); ok {
				_, pointer = function.Recv.List[0].Type.(*ast.StarExpr)
			}
			if pointer && !typ.pointer && !typ.addressable {
				continue
			}
			targets = append(targets, method)
		}
		return walker.compatible(targets), node.Sel.Name, false
	case *ast.FuncLit:
		return nil, "<anonymous>", false
	}
	return nil, "<dynamic>", false
}

func (walker *goCallWalk) methodTargets(typ goTypeReference, name string, seen map[string]bool) []*goDeclaration {
	key := typ.file.packageKey + "|" + typ.name
	if seen[key] || len(seen) >= 32 {
		return nil
	}
	seen[key] = true
	result := append([]*goDeclaration{}, walker.analyzer.methods[key+"|"+name]...)
	for _, declaration := range walker.analyzer.packageDeclarations(typ.file, typ.name) {
		general, ok := declaration.node.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			alias, ok := specification.(*ast.TypeSpec)
			if !ok || alias.Name.Name != typ.name || !alias.Assign.IsValid() {
				continue
			}
			other := *walker
			other.declaration = declaration
			target := other.typeReference(alias.Type)
			if target.file != nil && target.name != "" {
				result = appendUniqueGoDeclarations(result, walker.methodTargets(target, name, seen)...)
			}
		}
	}
	return result
}

func (walker *goCallWalk) callableTargets(declarations []*goDeclaration, name string) ([]*goDeclaration, string, bool) {
	targets := []*goDeclaration{}
	conversion := false
	for _, declaration := range walker.compatible(declarations) {
		if _, callable := declaration.node.(*ast.FuncDecl); callable {
			targets = append(targets, declaration)
		} else if general, ok := declaration.node.(*ast.GenDecl); ok && general.Tok == token.TYPE {
			conversion = true
		}
	}
	return targets, name, conversion
}

func appendUniqueGoDeclarations(items []*goDeclaration, additions ...*goDeclaration) []*goDeclaration {
	for _, addition := range additions {
		if !slices.Contains(items, addition) {
			items = append(items, addition)
		}
	}
	return items
}

func (walker *goCallWalk) recordCall(call *ast.CallExpr, scope goScope) {
	targets, name, conversion := walker.resolveCall(call.Fun, scope)
	if conversion {
		return
	}
	ids := []string{}
	for _, target := range targets {
		walker.references[target] = true
		if id := walker.graphIDs[target.symbolID]; id != "" {
			ids = append(ids, id)
		}
	}
	if walker.declaration.symbolID == "" {
		return
	}
	resolution := 0
	if len(ids) == 0 {
		resolution = 2
	} else if len(ids) > 1 {
		resolution = 1
	}
	reasons := []string{}
	for _, target := range targets {
		if target.file.variant != "" && target.file.variant != walker.declaration.file.variant || target.file.platform != "" && target.file.platform != walker.declaration.file.platform || target.file.architecture != "" && target.file.architecture != walker.declaration.file.architecture {
			resolution = 1
			reasons = append(reasons, "Go build constraints are not proven for this source-union comparison.")
		}
		if _, contract := target.node.(*ast.Field); contract {
			reasons = append(reasons, "The target is a Go interface contract; concrete runtime dispatch is not proven.")
		}
	}
	line := walker.declaration.file.position(call.Pos())
	walker.edges = append(walker.edges, GraphEdge{Kind: "call", SourceID: walker.graphIDs[walker.declaration.symbolID], TargetIDs: ids, Revision: walker.declaration.file.source.Revision, Line: &line, InvokedName: &name, Resolution: resolution, Reasons: unique(reasons)})
}

func (walker *goCallWalk) walk(root ast.Node, scope goScope) {
	if root == nil || walker.err != nil {
		return
	}
	switch node := root.(type) {
	case *ast.FieldList:
		if node == nil {
			return
		}
	case *ast.BlockStmt:
		if node == nil {
			return
		}
	}
	ast.Inspect(root, func(node ast.Node) bool {
		if walker.err = walker.context.Err(); walker.err != nil || node == nil {
			return false
		}
		switch current := node.(type) {
		case *ast.FuncDecl:
			walker.walk(current.Recv, scope)
			walker.walk(current.Type, scope)
			walker.walk(current.Body, scope)
			return false
		case *ast.Field:
			walker.walk(current.Type, scope)
			return false
		case *ast.TypeSpec:
			walker.walk(current.TypeParams, scope)
			walker.walk(current.Type, scope)
			return false
		case *ast.ValueSpec:
			walker.walk(current.Type, scope)
			for _, expression := range current.Values {
				walker.walk(expression, scope)
			}
			return false
		case *ast.BlockStmt:
			child := scope.child()
			for _, statement := range current.List {
				walker.walk(statement, child)
			}
			return false
		case *ast.FuncLit:
			child := scope.child()
			walker.bindFields(child, current.Type.Params)
			walker.bindFields(child, current.Type.Results)
			walker.walk(current.Type, child)
			walker.walk(current.Body, child)
			return false
		case *ast.AssignStmt:
			for _, expression := range current.Rhs {
				walker.walk(expression, scope)
			}
			for index, expression := range current.Lhs {
				if name, ok := expression.(*ast.Ident); ok && current.Tok == token.DEFINE {
					typ := goTypeReference{}
					if index < len(current.Rhs) {
						typ = walker.expressionType(current.Rhs[index], scope)
					}
					typ.addressable = true
					scope.bindings[name.Name] = typ
				} else {
					walker.walk(expression, scope)
				}
			}
			return false
		case *ast.DeclStmt:
			if general, ok := current.Decl.(*ast.GenDecl); ok {
				for _, specification := range general.Specs {
					if value, ok := specification.(*ast.ValueSpec); ok {
						for _, expression := range value.Values {
							walker.walk(expression, scope)
						}
						walker.walk(value.Type, scope)
						for index, name := range value.Names {
							typ := walker.typeReference(value.Type)
							if value.Type == nil && index < len(value.Values) {
								typ = walker.expressionType(value.Values[index], scope)
							}
							typ.addressable = true
							scope.bindings[name.Name] = typ
						}
					} else if typ, ok := specification.(*ast.TypeSpec); ok {
						walker.walk(typ.Type, scope)
						scope.bindings[typ.Name.Name] = goTypeReference{}
					}
				}
			}
			return false
		case *ast.IfStmt:
			child := scope.child()
			walker.walk(current.Init, child)
			walker.walk(current.Cond, child)
			walker.walk(current.Body, child)
			walker.walk(current.Else, child)
			return false
		case *ast.ForStmt:
			child := scope.child()
			walker.walk(current.Init, child)
			walker.walk(current.Cond, child)
			walker.walk(current.Body, child)
			walker.walk(current.Post, child)
			return false
		case *ast.SwitchStmt:
			child := scope.child()
			walker.walk(current.Init, child)
			walker.walk(current.Tag, child)
			walker.walk(current.Body, child)
			return false
		case *ast.TypeSwitchStmt:
			child := scope.child()
			walker.walk(current.Init, child)
			walker.walk(current.Assign, child)
			walker.walk(current.Body, child)
			return false
		case *ast.CaseClause:
			child := scope.child()
			for _, expression := range current.List {
				walker.walk(expression, child)
			}
			for _, statement := range current.Body {
				walker.walk(statement, child)
			}
			return false
		case *ast.CommClause:
			child := scope.child()
			walker.walk(current.Comm, child)
			for _, statement := range current.Body {
				walker.walk(statement, child)
			}
			return false
		case *ast.RangeStmt:
			walker.walk(current.X, scope)
			child := scope.child()
			for _, expression := range []ast.Expr{current.Key, current.Value} {
				if name, ok := expression.(*ast.Ident); ok && current.Tok == token.DEFINE {
					child.bindings[name.Name] = goTypeReference{}
				}
			}
			walker.walk(current.Body, child)
			return false
		case *ast.CallExpr:
			walker.recordCall(current, scope)
		case *ast.SelectorExpr:
			if alias, ok := current.X.(*ast.Ident); ok {
				if _, shadowed := scope.lookup(alias.Name); !shadowed {
					for _, candidate := range walker.analyzer.importFiles(walker.declaration.file, walker.declaration.file.imports[alias.Name]) {
						for _, declaration := range walker.analyzer.packageDeclarations(candidate, current.Sel.Name) {
							walker.references[declaration] = true
						}
					}
				}
			}
			walker.walk(current.X, scope)
			return false
		case *ast.Ident:
			if _, shadowed := scope.lookup(current.Name); !shadowed {
				for _, declaration := range walker.analyzer.packageDeclarations(walker.declaration.file, current.Name) {
					if declaration != walker.declaration {
						walker.references[declaration] = true
					}
				}
			}
		}
		return true
	})
}

package review

import (
	"context"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"strings"
)

type goDeclaration struct {
	file       *goSourceFile
	node       ast.Node
	names      []string
	owner      string
	symbolID   string
	source     CodeSlice
	resultType ast.Expr
}

type goSourceFile struct {
	source       SourceSnapshot
	lines        []string
	fset         *token.FileSet
	ast          *ast.File
	analysis     sourceAnalysis
	packageKey   string
	module       *goSourceModule
	imports      map[string]string
	dotImports   []string
	declarations []*goDeclaration
	ranges       []*goDeclaration
	rangeEnds    []int
	rangeVisited int
	variant      string
	platform     string
	architecture string
}

type goSourceAnalyzer struct {
	files        map[mrSourceKey]*goSourceFile
	ordered      []*goSourceFile
	modules      []*goSourceModule
	workspaces   map[mrSourceKey]string
	directories  map[mrSourceKey][]*goSourceFile
	importCache  map[string][]*goSourceFile
	declarations map[string][]*goDeclaration
	methods      map[string][]*goDeclaration
	parents      map[string]string
	limitations  []string
}

func newGoSourceAnalyzerContext(ctx context.Context, files []FileSnapshot) (*goSourceAnalyzer, error) {
	analyzer := &goSourceAnalyzer{files: map[mrSourceKey]*goSourceFile{}, directories: map[mrSourceKey][]*goSourceFile{}, importCache: map[string][]*goSourceFile{}, declarations: map[string][]*goDeclaration{}, methods: map[string][]*goDeclaration{}, parents: map[string]string{}, workspaces: map[mrSourceKey]string{}}
	if err := analyzer.readModules(ctx, files); err != nil {
		return nil, err
	}
	for _, file := range files {
		for _, source := range []*SourceSnapshot{file.Base, file.Head} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if source == nil || !isGoSource(source.Path) || file.SkipReason != "" || file.IsBinary || file.IsLFS || file.IsSubmodule {
				continue
			}
			parsed := analyzer.parseFile(*source)
			analyzer.files[mrSourceKey{source.Revision, source.Path}] = parsed
			analyzer.ordered = append(analyzer.ordered, parsed)
			directoryKey := mrSourceKey{source.Revision, path.Dir(source.Path)}
			analyzer.directories[directoryKey] = append(analyzer.directories[directoryKey], parsed)
		}
	}
	for _, file := range analyzer.ordered {
		analyzer.bindImports(file)
		if err := analyzer.indexFile(ctx, file); err != nil {
			return nil, err
		}
		file.indexDeclarationRanges()
	}
	return analyzer, ctx.Err()
}

func (analyzer *goSourceAnalyzer) parse(ctx context.Context, source SourceSnapshot) (sourceAnalysis, error) {
	if err := ctx.Err(); err != nil {
		return sourceAnalysis{}, err
	}
	if file := analyzer.files[mrSourceKey{source.Revision, source.Path}]; file != nil {
		return file.analysis, nil
	}
	return sourceAnalysis{}, nil
}

func (analyzer *goSourceAnalyzer) parseFile(source SourceSnapshot) *goSourceFile {
	file := &goSourceFile{source: source, fset: token.NewFileSet(), imports: map[string]string{}}
	if len(source.Content) > 4*1024*1024 {
		file.analysis.limitations = []string{source.Path + ": Go AST input exceeds the 4 MiB parser limit; source fallback is used."}
		return file
	}
	file.lines = lines(source.Content)
	file.module = analyzer.moduleFor(source.Revision, source.Path)
	parsed, err := parser.ParseFile(file.fset, source.Path, source.Content, parser.ParseComments|parser.SkipObjectResolution|parser.AllErrors)
	if err != nil {
		// Parser diagnostics may quote source text, so only retain the failure category.
		file.analysis.limitations = []string{source.Path + ": Go syntax is incomplete; review uses source fallback and does not prove dependencies."}
		return file
	}
	file.ast = parsed
	file.packageKey = fmt.Sprintf("%d|%s|%s", source.Revision, path.Dir(source.Path), parsed.Name.Name)
	file.variant, file.platform, file.architecture = goBuildVariant(source)

	return file
}

func goBuildVariant(source SourceSnapshot) (string, string, string) {
	variant := []string{}
	for line := range strings.SplitSeq(source.Content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			break
		}
		if constraint.IsGoBuild(strings.TrimSpace(line)) {
			if expression, err := constraint.Parse(strings.TrimSpace(line)); err == nil {
				variant = append(variant, expression.String())
			}
		}
	}
	platform, architecture := "", ""
	name := strings.TrimSuffix(strings.TrimSuffix(path.Base(source.Path), ".go"), "_test")
	parts := strings.Split(name, "_")
	last := len(parts) - 1
	if last > 0 && slices.Contains([]string{"386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm"}, parts[last]) {
		architecture = parts[last]
		last--
	}
	if last > 0 && slices.Contains([]string{"aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows"}, parts[last]) {
		platform = parts[last]
	}
	return strings.Join(variant, " && "), platform, architecture
}

func (file *goSourceFile) position(position token.Pos) int {
	return file.fset.PositionFor(position, false).Line
}

func (file *goSourceFile) code(node ast.Node, comments *ast.CommentGroup) CodeSlice {
	first := file.position(node.Pos())
	if comments != nil {
		first = min(first, file.position(comments.Pos()))
	}
	return file.slice(first, file.position(node.End()))
}

func (file *goSourceFile) slice(first, last int) CodeSlice {
	return CodeSlice{Path: file.source.Path, Revision: file.source.Revision, StartLine: first, EndLine: last, SymbolStartLine: first, SymbolEndLine: last, Content: strings.Join(file.lines[first-1:last], "\n"), IsComplete: true}
}

func (file *goSourceFile) text(node ast.Node) string {
	first, last := file.fset.PositionFor(node.Pos(), false).Offset, file.fset.PositionFor(node.End(), false).Offset
	return file.source.Content[first:last]
}

func (analyzer *goSourceAnalyzer) indexFile(ctx context.Context, file *goSourceFile) error {
	if file.ast == nil {
		return ctx.Err()
	}
	addSymbol := func(declaration *goDeclaration, name, owner string, kind int, signature string) int {
		key := "go:" + strings.SplitN(file.packageKey, "|", 2)[1] + ":" + owner + ":" + name
		if name == "init" {
			key += fmt.Sprintf(":%s:%d", file.source.Path, declaration.source.StartLine)
		}
		id := Hash(fmt.Sprintf("go|%d|%s|%s|%d|%d", file.source.Revision, file.source.Path, key, declaration.source.StartLine, declaration.node.Pos()))
		node := GraphNode{SourceSymbolID: id, Path: file.source.Path, Revision: file.source.Revision, Name: name, Signature: signature, Kind: kind, StartLine: declaration.source.StartLine, EndLine: declaration.source.EndLine}
		if owner != "" {
			node.DeclaringType = &owner
		}
		declaration.symbolID = id
		file.analysis.symbols = append(file.analysis.symbols, sourceSymbol{node: node, key: key, content: declaration.source.Content})
		return len(file.analysis.symbols) - 1
	}
	addDeclaration := func(declaration *goDeclaration) {
		file.declarations = append(file.declarations, declaration)
		for _, name := range declaration.names {
			index := analyzer.declarations
			key := file.packageKey + "|" + name
			if declaration.owner != "" {
				index = analyzer.methods
				key = file.packageKey + "|" + declaration.owner + "|" + name
			}
			index[key] = append(index[key], declaration)
		}
	}
	packageEnd := file.position(file.ast.Name.End())
	for _, declaration := range file.ast.Decls {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node := declaration.(type) {
		case *ast.FuncDecl:
			owner := ""
			if node.Recv != nil && len(node.Recv.List) > 0 {
				owner = goTypeName(node.Recv.List[0].Type)
			}
			item := &goDeclaration{file: file, node: node, names: []string{node.Name.Name}, owner: owner, source: file.code(node, node.Doc)}
			if node.Type.Results != nil && len(node.Type.Results.List) > 0 {
				item.resultType = node.Type.Results.List[0].Type
			}
			signature := file.text(node)
			if node.Body != nil {
				signature = strings.TrimSpace(signature[:file.fset.PositionFor(node.Body.Pos(), false).Offset-file.fset.PositionFor(node.Pos(), false).Offset])
			}
			index := addSymbol(item, node.Name.Name, owner, 4, signature)
			file.analysis.units = append(file.analysis.units, sourceUnit{file.analysis.symbols[index].node.SourceSymbolID, "method", item.source, index})
			addDeclaration(item)
		case *ast.GenDecl:
			if node.Tok == token.IMPORT {
				packageEnd = max(packageEnd, file.position(node.End()))
				continue
			}
			item := &goDeclaration{file: file, node: node, source: file.code(node, node.Doc)}
			symbolIndex := -1
			for _, specification := range node.Specs {
				switch spec := specification.(type) {
				case *ast.TypeSpec:
					item.names = append(item.names, spec.Name.Name)
					kind := 0
					if _, ok := spec.Type.(*ast.InterfaceType); ok {
						kind = 1
					}
					typeDeclaration := &goDeclaration{file: file, node: spec, names: []string{spec.Name.Name}, source: file.code(spec, spec.Doc)}
					index := addSymbol(typeDeclaration, spec.Name.Name, "", kind, file.text(spec))
					if len(node.Specs) == 1 {
						symbolIndex = index
						item.symbolID = typeDeclaration.symbolID
					}
					if contract, ok := spec.Type.(*ast.InterfaceType); ok {
						for _, field := range contract.Methods.List {
							if len(field.Names) != 1 {
								continue
							}
							method := &goDeclaration{file: file, node: field, names: []string{field.Names[0].Name}, owner: spec.Name.Name, source: file.code(field, field.Doc)}
							addSymbol(method, field.Names[0].Name, spec.Name.Name, 4, file.text(field))
							analyzer.parents[method.symbolID] = typeDeclaration.symbolID
							addDeclaration(method)
						}
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						item.names = append(item.names, name.Name)
					}
				}
			}
			key := "go:declaration:" + node.Tok.String() + ":" + strings.Join(item.names, ",")
			if symbolIndex >= 0 {
				key = item.symbolID
			}
			file.analysis.units = append(file.analysis.units, sourceUnit{key, "declaration", item.source, symbolIndex})
			addDeclaration(item)
		}
	}
	file.analysis.units = append(file.analysis.units, sourceUnit{"go:imports", "imports", file.slice(1, packageEnd), -1})
	file.analysis.units = mergeGoUnits(file.analysis.units, file.analysis.symbols)
	return ctx.Err()
}

func mergeGoUnits(units []sourceUnit, symbols []sourceSymbol) []sourceUnit {
	stableKey := func(unit sourceUnit) string {
		if unit.symbol >= 0 {
			return symbols[unit.symbol].key
		}
		return unit.key
	}
	slices.SortStableFunc(units, func(a, b sourceUnit) int { return a.source.StartLine - b.source.StartLine })
	result := []sourceUnit{}
	for _, unit := range units {
		if len(result) > 0 && result[len(result)-1].source.EndLine >= unit.source.StartLine {
			prior := &result[len(result)-1]
			if unit.source.EndLine > prior.source.EndLine {
				ls := lines(unit.source.Content)
				prior.source.Content += "\n" + strings.Join(ls[prior.source.EndLine-unit.source.StartLine+1:], "\n")
				prior.source.EndLine = unit.source.EndLine
				prior.source.SymbolEndLine = unit.source.EndLine
			}
			prior.key = Hash(stableKey(*prior) + "+" + stableKey(unit))
			prior.kind, prior.symbol = "declaration", -1
			continue
		}
		result = append(result, unit)
	}
	return result
}

func goTypeName(expression ast.Expr) string {
	switch node := expression.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return goTypeName(node.X)
	case *ast.IndexExpr:
		return goTypeName(node.X)
	case *ast.IndexListExpr:
		return goTypeName(node.X)
	case *ast.SelectorExpr:
		return goTypeName(node.X) + "." + node.Sel.Name
	case *ast.ParenExpr:
		return goTypeName(node.X)
	}
	return ""
}

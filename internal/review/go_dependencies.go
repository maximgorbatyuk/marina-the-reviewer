package review

import (
	"context"
	"go/ast"
)

func goRequiredSymbols(symbols []sourceSymbol, references map[*goDeclaration][]*goDeclaration) map[string]bool {
	changed := map[string]bool{}
	for _, symbol := range symbols {
		if symbol.node.Change != 0 {
			changed[symbol.node.SourceSymbolID] = true
		}
	}
	result := map[string]bool{}
	for declaration, dependencies := range references {
		if !changed[declaration.symbolID] {
			continue
		}
		for _, dependency := range dependencies {
			if dependency.symbolID != "" {
				result[dependency.symbolID] = true
			}
		}
	}
	return result
}

func (analyzer *goSourceAnalyzer) decorateDocumentContext(ctx context.Context, document *Document, references map[*goDeclaration][]*goDeclaration) error {
	if len(analyzer.ordered) == 0 {
		return ctx.Err()
	}
	for index := range document.Blocks {
		block := &document.Blocks[index]
		seen := map[contextSliceKey]bool{}
		add := func(source CodeSlice) {
			key := contextKey(source)
			if !seen[key] && !sliceContains(block.Base, source) && !sliceContains(block.Head, source) {
				block.StructuralContext = append(block.StructuralContext, source)
				seen[key] = true
			}
		}
		for _, source := range block.StructuralContext {
			seen[contextKey(source)] = true
		}
		for _, source := range []*CodeSlice{block.Base, block.Head} {
			if source == nil || !isGoSource(source.Path) {
				continue
			}
			file := analyzer.files[mrSourceKey{source.Revision, source.Path}]
			if file == nil || file.ast == nil {
				continue
			}
			pending := []*goDeclaration{}
			for _, declaration := range file.declarations {
				if declaration.source.StartLine <= source.EndLine && declaration.source.EndLine >= source.StartLine {
					pending = append(pending, declaration)
				}
			}
			visited := map[*goDeclaration]bool{}
			for len(pending) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				declaration := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if visited[declaration] {
					continue
				}
				visited[declaration] = true
				for _, dependency := range references[declaration] {
					if _, function := dependency.node.(*ast.FuncDecl); function {
						if declaration.symbolID == "" {
							add(dependency.source)
						}
						for _, nested := range references[dependency] {
							if _, callable := nested.node.(*ast.FuncDecl); !callable {
								add(nested.source)
								pending = append(pending, nested)
							}
						}
						continue
					}
					add(dependency.source)
					pending = append(pending, dependency)
				}
			}
		}
	}
	return ctx.Err()
}

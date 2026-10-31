package review

import (
	"context"
	"path"
	"strings"
)

type sourceSymbol struct {
	node                                        GraphNode
	key, content                                string
	startToken, openToken, endToken, parameters int
}

type sourceUnit struct {
	key, kind string
	source    CodeSlice
	symbol    int
}

type javaSymbol = sourceSymbol
type javaUnit = sourceUnit

type sourceAnalysis struct {
	symbols     []sourceSymbol
	units       []sourceUnit
	limitations []string
}

type sourceAnalyzer interface {
	parse(context.Context, SourceSnapshot) (sourceAnalysis, error)
}

type javaSourceAnalyzer struct{ selection int }

func (analyzer javaSourceAnalyzer) parse(ctx context.Context, source SourceSnapshot) (sourceAnalysis, error) {
	var result sourceAnalysis
	var err error
	if analyzer.selection >= 2 {
		result.symbols, result.units, result.limitations, err = readMRJavaContext(ctx, source)
	} else {
		result.symbols, result.units, result.limitations, err = readJavaWithUnicodePolicyContext(ctx, source, false)
	}
	return result, err
}

type fallbackSourceAnalyzer struct{ selection int }

func (analyzer fallbackSourceAnalyzer) parse(ctx context.Context, source SourceSnapshot) (sourceAnalysis, error) {
	result := sourceAnalysis{}
	if analyzer.selection >= 2 {
		var err error
		result.symbols, err = readLanguageNeutralFunctionsContext(ctx, source)
		if err != nil {
			return result, err
		}
		result.limitations = []string{source.Path + ": semantic Java resolution is unavailable; symbol ranges use the language-neutral fallback."}
	}
	return result, nil
}

func isGoSource(name string) bool { return strings.EqualFold(path.Ext(name), ".go") }

func isGoMetadata(name string) bool {
	return path.Base(name) == "go.mod" || path.Base(name) == "go.work"
}

func goContextPath(name string) bool {
	return (isGoSource(name) || isGoMetadata(name)) && !strings.Contains("/"+name, "/vendor/")
}

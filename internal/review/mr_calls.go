package review

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

type mrTypeKey struct {
	revision Revision
	name     string
}

type mrMethodKey struct {
	revision Revision
	owner    string
	name     string
	arity    int
}

type mrSourceKey struct {
	revision Revision
	path     string
}

type mrCallResolver struct {
	symbols     []javaSymbol
	methods     map[mrMethodKey][]int
	types       map[mrTypeKey][]int
	bases       map[string][]string
	variables   map[string]map[string]string
	graphIDs    map[string]string
	implemented map[mrTypeKey][]string
	hasBody     map[string]bool
}

var mrBasePattern = regexp.MustCompile(`\b(?:extends|implements)\s+`)
var mrVariablePattern = regexp.MustCompile(`(?:^|[;{}])\s*(?:(?:public|protected|private|static|final|volatile|transient)\s+)*([A-Za-z_][A-Za-z0-9_.$]*(?:\s*<[^;={}()]+>)?(?:\[\])?)\s+([a-zA-Z_][A-Za-z0-9_]*)\s*[=;,]`)
var mrAnnotationPattern = regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_.]*(?:\s*\([^)]*\))?\s*`)
var mrModifierPattern = regexp.MustCompile(`\b(?:public|protected|private|static|final|abstract|default|synchronized|native|strictfp)\b`)

func mrBaseTypes(symbol javaSymbol) []string {
	header := symbol.content
	if opening := javaBodyOpening(header); opening >= 0 {
		header = header[:opening]
	}
	matches := mrBasePattern.FindAllStringIndex(header, -1)
	result := []string{}
	for i, match := range matches {
		end := len(header)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		for _, base := range splitJavaTopLevel(header[match[1]:end]) {
			base = strings.TrimSpace(base)
			if base != "" && !slices.Contains(result, base) {
				result = append(result, base)
			}
		}
	}
	return result
}

func splitJavaTopLevel(value string) []string {
	result := []string{}
	start, depth := 0, 0
	for i := range len(value) {
		switch value[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				result = append(result, value[start:i])
				start = i + 1
			}
		}
	}
	if start < len(value) {
		result = append(result, value[start:])
	}
	return result
}

func simpleJavaType(value string) string {
	if generic := strings.IndexByte(value, '<'); generic >= 0 {
		value = value[:generic]
	}
	value = strings.TrimSpace(strings.ReplaceAll(value, "[]", ""))
	if dot := strings.LastIndexByte(value, '.'); dot >= 0 {
		value = value[dot+1:]
	}
	return value
}

type mrParameter struct{ name, declaredType string }

func mrParameters(symbol javaSymbol) []mrParameter {
	header := symbol.content
	if opening := javaBodyOpening(header); opening >= 0 {
		header = header[:opening]
	}
	header = mrAnnotationPattern.ReplaceAllString(sanitizeJava(header), "")
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol.node.Name) + `\s*\(`)
	match := pattern.FindStringIndex(header)
	if match == nil {
		return nil
	}
	end := strings.LastIndexByte(header, ')')
	if end < match[1] {
		return nil
	}
	result := []mrParameter{}
	for _, value := range splitJavaTopLevel(header[match[1]:end]) {
		value = mrAnnotationPattern.ReplaceAllString(value, "")
		value = strings.TrimSpace(strings.ReplaceAll(value, "final ", ""))
		parts := strings.Fields(value)
		if len(parts) < 2 {
			continue
		}
		name := strings.ReplaceAll(parts[len(parts)-1], "...", "")
		declaredType := strings.ReplaceAll(strings.Join(parts[:len(parts)-1], " "), "...", "[]")
		result = append(result, mrParameter{name, declaredType})
	}
	return result
}

func normalizeMRSymbol(symbol *javaSymbol) {
	if symbol.node.Kind != 4 {
		kind := []string{"class", "interface", "record", "enum"}[symbol.node.Kind]
		symbol.node.Signature = kind + " " + symbol.node.Name
		if bases := mrBaseTypes(*symbol); len(bases) > 0 {
			symbol.node.Signature += " : " + strings.Join(bases, ", ")
		}
		return
	}
	parameters := mrParameters(*symbol)
	parameterTypes, signatures := []string{}, []string{}
	for _, parameter := range parameters {
		parameterTypes = append(parameterTypes, simpleJavaType(parameter.declaredType))
		signatures = append(signatures, parameter.declaredType+" "+parameter.name)
	}
	owner := ""
	if symbol.node.DeclaringType != nil {
		owner = *symbol.node.DeclaringType
	}
	symbol.key = "method:" + owner + ":" + symbol.node.Name + "(" + strings.Join(parameterTypes, ",") + ")"
	symbol.parameters = len(parameters)
	header := symbol.content
	if opening := javaBodyOpening(header); opening >= 0 {
		header = header[:opening]
	}
	header = mrAnnotationPattern.ReplaceAllString(sanitizeJava(header), "")
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol.node.Name) + `\s*\(`)
	match := pattern.FindStringIndex(header)
	prefix := ""
	if match != nil {
		prefix = strings.TrimSpace(mrModifierPattern.ReplaceAllString(header[:match[0]], ""))
	}
	if owner == symbol.node.Name {
		prefix = "<constructor>"
	} else if prefix == "" {
		prefix = "<unknown-return>"
	} else {
		prefix = strings.Join(strings.Fields(prefix), " ")
	}
	symbol.node.Signature = prefix + " " + symbol.node.Name + "(" + strings.Join(signatures, ", ") + ")"
}

func newMRCallResolver(symbols []javaSymbol, files []FileSnapshot, graphIDs map[string]string) *mrCallResolver {
	result, _ := newMRCallResolverContext(context.Background(), symbols, files, graphIDs)
	return result
}
func newMRCallResolverContext(ctx context.Context, symbols []javaSymbol, files []FileSnapshot, graphIDs map[string]string) (*mrCallResolver, error) {
	r := &mrCallResolver{symbols: symbols, methods: map[mrMethodKey][]int{}, types: map[mrTypeKey][]int{}, bases: map[string][]string{}, variables: map[string]map[string]string{}, graphIDs: graphIDs, implemented: map[mrTypeKey][]string{}, hasBody: map[string]bool{}}
	sources := map[mrSourceKey][]string{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, source := range []*SourceSnapshot{file.Base, file.Head} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if source != nil {
				sources[mrSourceKey{source.Revision, source.Path}] = lines(source.Content)
			}
		}
	}
	type ownerKey struct {
		mrSourceKey
		name string
	}
	methodsByOwner := map[ownerKey][]javaSymbol{}
	for i, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if symbol.node.Kind == 4 {
			r.hasBody[symbol.node.SourceSymbolID] = javaBodyOpening(symbol.content) >= 0
			if symbol.node.DeclaringType != nil {
				owner := *symbol.node.DeclaringType
				key := mrMethodKey{symbol.node.Revision, owner, symbol.node.Name, symbol.parameters}
				r.methods[key] = append(r.methods[key], i)
				key.arity = -1
				r.methods[key] = append(r.methods[key], i)
				fileOwner := ownerKey{mrSourceKey{symbol.node.Revision, symbol.node.Path}, owner}
				methodsByOwner[fileOwner] = append(methodsByOwner[fileOwner], symbol)
			}
		} else {
			key := mrTypeKey{symbol.node.Revision, symbol.node.Name}
			r.types[key] = append(r.types[key], i)
			r.bases[symbol.node.SourceSymbolID] = mrBaseTypes(symbol)
			for _, base := range r.bases[symbol.node.SourceSymbolID] {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				key := mrTypeKey{symbol.node.Revision, simpleJavaType(base)}
				if !slices.Contains(r.implemented[key], symbol.node.Name) {
					r.implemented[key] = append(r.implemented[key], symbol.node.Name)
				}
			}
		}
	}
	fields := map[string]map[string]string{}
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if symbol.node.Kind == 4 {
			continue
		}
		values := map[string]string{}
		source := sources[mrSourceKey{symbol.node.Revision, symbol.node.Path}]
		methods := methodsByOwner[ownerKey{mrSourceKey{symbol.node.Revision, symbol.node.Path}, symbol.node.Name}]
		methodIndex := 0
		for line := symbol.node.StartLine; line <= symbol.node.EndLine && line <= len(source); line++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for methodIndex < len(methods) && methods[methodIndex].node.EndLine < line {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				methodIndex++
			}
			if methodIndex < len(methods) && methods[methodIndex].node.StartLine <= line && methods[methodIndex].node.EndLine >= line {
				continue
			}
			readMRVariables(source[line-1], values)
		}
		fields[symbol.node.SourceSymbolID] = values
	}
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if symbol.node.Kind != 4 {
			continue
		}
		values := map[string]string{}
		if symbol.node.DeclaringType != nil {
			for _, index := range r.types[mrTypeKey{symbol.node.Revision, *symbol.node.DeclaringType}] {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				owner := symbols[index]
				if owner.node.Path == symbol.node.Path && owner.node.StartLine <= symbol.node.StartLine && owner.node.EndLine >= symbol.node.EndLine {
					for name, declaredType := range fields[owner.node.SourceSymbolID] {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						values[name] = declaredType
					}
					break
				}
			}
		}
		parameters := map[string]bool{}
		for _, parameter := range mrParameters(symbol) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			values[parameter.name] = simpleJavaType(parameter.declaredType)
			parameters[parameter.name] = true
		}
		locals := map[string]string{}
		for _, line := range lines(symbol.content) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			readMRVariables(line, locals)
		}
		for name, declaredType := range locals {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !parameters[name] {
				values[name] = declaredType
			}
		}
		r.variables[symbol.node.SourceSymbolID] = values
	}
	return r, ctx.Err()
}

func readMRVariables(source string, values map[string]string) {
	for _, match := range mrVariablePattern.FindAllStringSubmatch(source, -1) {
		if _, found := values[match[2]]; !found {
			values[match[2]] = simpleJavaType(match[1])
		}
	}
}

func (r *mrCallResolver) resolve(source javaSymbol, name, receiver string, arity int) ([]string, *string, int) {
	var receiverType *string
	if receiver == "" || receiver == "this" {
		receiverType = source.node.DeclaringType
	} else if declaredType, found := r.variables[source.node.SourceSymbolID][receiver]; found {
		receiverType = &declaredType
	}
	owners := []string{}
	if receiverType != nil && strings.TrimSpace(*receiverType) != "" {
		owners = append(owners, simpleJavaType(*receiverType))
	} else if receiver != "" && unicode.IsUpper(rune(receiver[0])) {
		owners = append(owners, receiver)
	} else if (receiver == "" || receiver == "this") && source.node.DeclaringType != nil {
		owners = append(owners, *source.node.DeclaringType)
		for _, index := range r.types[mrTypeKey{source.node.Revision, *source.node.DeclaringType}] {
			for _, base := range r.bases[r.symbols[index].node.SourceSymbolID] {
				owners = append(owners, simpleJavaType(base))
			}
		}
	}
	direct := []int{}
	for _, owner := range owners {
		direct = append(direct, r.methods[mrMethodKey{source.node.Revision, owner, name, arity}]...)
	}
	slices.Sort(direct)
	direct = slices.Compact(direct)
	targets := append([]int{}, direct...)
	for _, index := range direct {
		target := r.symbols[index]
		owner := *target.node.DeclaringType
		types := r.types[mrTypeKey{source.node.Revision, owner}]
		if len(types) == 0 || r.symbols[types[0]].node.Kind != 1 {
			continue
		}
		implementations := []int{}
		for _, implementation := range r.implemented[mrTypeKey{source.node.Revision, owner}] {
			implementations = append(implementations, r.methods[mrMethodKey{source.node.Revision, implementation, name, arity}]...)
		}
		slices.Sort(implementations)
		targets = append(targets, implementations...)
	}
	if len(targets) == 0 && slices.Contains([]string{"findById", "findAll", "save", "saveAll", "delete", "deleteById", "deleteAll", "existsById", "count", "flush", "saveAndFlush", "getReferenceById"}, name) {
		receiverName := receiver
		if receiverType != nil {
			receiverName = *receiverType
		}
		declarations := r.types[mrTypeKey{source.node.Revision, simpleJavaType(receiverName)}]
		framework := false
		for _, index := range declarations {
			for _, base := range r.bases[r.symbols[index].node.SourceSymbolID] {
				framework = framework || strings.Contains(base, "JpaRepository") || strings.Contains(base, "CrudRepository") || strings.Contains(base, "PagingAndSortingRepository")
			}
		}
		if framework {
			targets = declarations
		}
	}
	ids, seen, executable := []string{}, map[int]bool{}, 0
	for _, index := range targets {
		if seen[index] {
			continue
		}
		seen[index] = true
		target := r.symbols[index]
		ids = append(ids, r.graphIDs[target.node.SourceSymbolID])
		isInterface := false
		if target.node.DeclaringType != nil {
			for _, owner := range r.types[mrTypeKey{target.node.Revision, *target.node.DeclaringType}] {
				isInterface = isInterface || r.symbols[owner].node.Kind == 1
			}
		}
		if r.hasBody[target.node.SourceSymbolID] || !isInterface {
			executable++
		}
	}
	resolution := 2
	if executable == 1 {
		resolution = 0
	} else if executable > 1 {
		resolution = 1
	}
	return ids, receiverType, resolution
}

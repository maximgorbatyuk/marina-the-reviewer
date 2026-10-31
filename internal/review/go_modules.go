package review

import (
	"context"
	"path"
	"strings"

	"golang.org/x/mod/modfile"
)

type goSourceModule struct {
	revision     Revision
	directory    string
	name         string
	replacements map[string]string
}

func localGoDirectory(base, value string) (string, bool) {
	if path.IsAbs(value) || strings.ContainsAny(value, "\\\x00:") {
		return "", false
	}
	value = path.Clean(path.Join(base, value))
	return value, value != ".." && !strings.HasPrefix(value, "../")
}

func (analyzer *goSourceAnalyzer) readModules(ctx context.Context, files []FileSnapshot) error {
	workspaceReplacements := map[mrSourceKey]map[string]string{}
	for _, file := range files {
		for _, source := range []*SourceSnapshot{file.Base, file.Head} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if source == nil || !isGoMetadata(source.Path) {
				continue
			}
			if len(source.Content) > 4*1024*1024 {
				analyzer.limitations = append(analyzer.limitations, source.Path+": Go metadata exceeds the 4 MiB parser limit.")
				continue
			}
			directory := path.Dir(source.Path)
			if path.Base(source.Path) == "go.work" {
				work, err := modfile.ParseWork(source.Path, []byte(source.Content), nil)
				if err != nil {
					analyzer.limitations = append(analyzer.limitations, source.Path+": Go workspace metadata could not be parsed.")
					continue
				}
				workspaceReplacements[mrSourceKey{source.Revision, directory}] = analyzer.localReplacements(*source, work.Replace)
				for _, use := range work.Use {
					if target, ok := localGoDirectory(directory, use.Path); ok {
						analyzer.workspaces[mrSourceKey{source.Revision, target}] = directory
					} else {
						analyzer.limitations = append(analyzer.limitations, source.Path+": Go workspace source outside the pinned tree is unavailable.")
					}
				}
				continue
			}
			module, err := modfile.Parse(source.Path, []byte(source.Content), nil)
			if err != nil || module.Module == nil {
				analyzer.limitations = append(analyzer.limitations, source.Path+": Go module metadata could not be parsed.")
				continue
			}
			item := &goSourceModule{revision: source.Revision, directory: directory, name: module.Module.Mod.Path, replacements: map[string]string{}}
			item.replacements = analyzer.localReplacements(*source, module.Replace)
			analyzer.modules = append(analyzer.modules, item)
		}
	}
	for _, module := range analyzer.modules {
		if workspace, found := analyzer.workspaces[mrSourceKey{module.revision, module.directory}]; found {
			for name, target := range workspaceReplacements[mrSourceKey{module.revision, workspace}] {
				module.replacements[name] = target
			}
		}
	}
	return ctx.Err()
}

func (analyzer *goSourceAnalyzer) localReplacements(source SourceSnapshot, replacements []*modfile.Replace) map[string]string {
	result := map[string]string{}
	versioned := map[string]bool{}
	for _, replacement := range replacements {
		result[replacement.Old.Path] = ""
		if replacement.New.Version != "" || replacement.Old.Version != "" {
			versioned[replacement.Old.Path] = true
			analyzer.limitations = append(analyzer.limitations, source.Path+": version-dependent Go replacements are not proven by the local source index.")
			continue
		}
		if target, ok := localGoDirectory(path.Dir(source.Path), replacement.New.Path); ok {
			result[replacement.Old.Path] = target
		} else {
			analyzer.limitations = append(analyzer.limitations, source.Path+": Go replacement source outside the pinned tree is unavailable.")
		}
	}
	for name := range versioned {
		result[name] = ""
	}
	return result
}

func (analyzer *goSourceAnalyzer) moduleFor(revision Revision, name string) *goSourceModule {
	var best *goSourceModule
	for _, module := range analyzer.modules {
		if module.revision == revision && (module.directory == "." || strings.HasPrefix(name, module.directory+"/")) && (best == nil || len(module.directory) > len(best.directory)) {
			best = module
		}
	}
	return best
}

func (analyzer *goSourceAnalyzer) importFiles(file *goSourceFile, importPath string) []*goSourceFile {
	if file.module == nil {
		return nil
	}
	cacheKey := file.packageKey + "|" + importPath
	if cached, found := analyzer.importCache[cacheKey]; found {
		return cached
	}
	analyzer.importCache[cacheKey] = nil
	directory := ""
	longest := 0
	blocked := false
	for prefix, target := range file.module.replacements {
		if (importPath == prefix || strings.HasPrefix(importPath, prefix+"/")) && len(prefix) > longest {
			longest = len(prefix)
			blocked = target == ""
			directory = path.Join(target, strings.TrimPrefix(importPath, prefix))
		}
	}
	if blocked {
		return nil
	}
	if directory == "" {
		for _, module := range analyzer.modules {
			if module.revision != file.source.Revision || importPath != module.name && !strings.HasPrefix(importPath, module.name+"/") {
				continue
			}
			workspace, inWorkspace := analyzer.workspaces[mrSourceKey{module.revision, module.directory}]
			ownWorkspace, ownInWorkspace := analyzer.workspaces[mrSourceKey{file.module.revision, file.module.directory}]
			if module != file.module && (!inWorkspace || !ownInWorkspace || workspace != ownWorkspace) {
				continue
			}
			if len(module.name) > longest {
				longest = len(module.name)
				directory = path.Join(module.directory, strings.TrimPrefix(importPath, module.name))
			}
		}
	}
	if directory == "" {
		return nil
	}
	result := []*goSourceFile{}
	for _, candidate := range analyzer.directories[mrSourceKey{file.source.Revision, directory}] {
		if candidate.ast != nil && candidate.source.Revision == file.source.Revision && path.Dir(candidate.source.Path) == directory && !strings.HasSuffix(candidate.ast.Name.Name, "_test") && !strings.HasSuffix(candidate.source.Path, "_test.go") {
			result = append(result, candidate)
		}
	}
	analyzer.importCache[cacheKey] = result
	return result
}

func (analyzer *goSourceAnalyzer) importPackageName(file *goSourceFile, importPath string) string {
	for _, candidate := range analyzer.importFiles(file, importPath) {
		return candidate.ast.Name.Name
	}
	return path.Base(importPath)
}

func (analyzer *goSourceAnalyzer) bindImports(file *goSourceFile) {
	if file.ast == nil {
		return
	}
	file.imports = map[string]string{}
	for _, specification := range file.ast.Imports {
		importPath := strings.Trim(specification.Path.Value, "\"`")
		name := analyzer.importPackageName(file, importPath)
		if specification.Name != nil {
			name = specification.Name.Name
		}
		if name != "." && name != "_" {
			file.imports[name] = importPath
		}
	}
}

func (analyzer *goSourceAnalyzer) packageDeclarations(file *goSourceFile, name string) []*goDeclaration {
	result := append([]*goDeclaration{}, analyzer.declarations[file.packageKey+"|"+name]...)
	if strings.HasSuffix(file.source.Path, "_test.go") && strings.HasSuffix(file.ast.Name.Name, "_test") {
		return result
	}
	return result
}

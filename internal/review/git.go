package review

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func gitRead(ctx context.Context, repo string, args ...string) ([]byte, error) {
	a := []string{"--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-C", repo}
	a = append(a, args...)
	c := exec.CommandContext(ctx, "git", a...)
	c.Env = gitEnvironment()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		return nil, fmt.Errorf("git %s failed: %w", args[0], e)
	}
	return b, nil
}
func gitEnvironment() []string {
	env := []string{}
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1")
}
func pinnedDiffView(ctx context.Context, repo, head string) (string, string, error) {
	objects, e := gitRead(ctx, repo, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if e != nil {
		return "", "", e
	}
	format, e := gitRead(ctx, repo, "rev-parse", "--show-object-format")
	if e != nil {
		return "", "", e
	}
	temp, e := os.MkdirTemp("", "marinadoreview-objects-")
	if e != nil {
		return "", "", e
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temp)
		}
	}()
	for _, dir := range []string{"objects", "refs"} {
		if e = os.Mkdir(filepath.Join(temp, dir), 0700); e != nil {
			return "", "", e
		}
	}
	config := "[core]\nrepositoryformatversion=0\nbare=true\n"
	if strings.TrimSpace(string(format)) == "sha256" {
		config = "[core]\nrepositoryformatversion=1\nbare=true\n[extensions]\nobjectformat=sha256\n"
	}
	if e = os.WriteFile(filepath.Join(temp, "config"), []byte(config), 0600); e != nil {
		return "", "", e
	}
	if e = os.WriteFile(filepath.Join(temp, "HEAD"), []byte(head+"\n"), 0600); e != nil {
		return "", "", e
	}
	cleanup = false
	return temp, strings.TrimSpace(string(objects)), nil
}
func gitPinnedDiff(ctx context.Context, view, objects, head string, args ...string) ([]byte, error) {
	command := []string{"--no-optional-locks", "--git-dir=" + view, "-c", "core.attributesFile=/dev/null"}
	command = append(command, args...)
	c := exec.CommandContext(ctx, "git", command...)
	c.Dir = view
	c.Env = append(gitEnvironment(), "GIT_OBJECT_DIRECTORY="+objects, "GIT_ATTR_SOURCE="+head)
	b, e := c.Output()
	if e != nil {
		return nil, fmt.Errorf("pinned Git diff failed: %w", e)
	}
	return b, nil
}
func Prepare(ctx context.Context, o PrepareOptions) (Snapshot, error) {
	var s Snapshot
	repo, e := filepath.Abs(o.Repository)
	if e != nil {
		return s, e
	}
	if o.Repository == "" {
		repo, e = os.Getwd()
		if e != nil {
			return s, e
		}
	}
	resolve := func(ref string) (string, error) {
		if strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, 0) {
			return "", fmt.Errorf("invalid Git revision")
		}
		b, e := gitRead(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		return strings.TrimSpace(string(b)), e
	}
	if o.Base == "" {
		return s, fmt.Errorf("base revision is required")
	}
	if o.Head == "" {
		o.Head = "HEAD"
	}
	base, e := resolve(o.Base)
	if e != nil {
		return s, e
	}
	head, e := resolve(o.Head)
	if e != nil {
		return s, e
	}
	start := base
	if o.Start != "" {
		start, e = resolve(o.Start)
		if e != nil {
			return s, e
		}
	}
	if !o.ExactBase {
		b, e := gitRead(ctx, repo, "merge-base", base, head)
		if e != nil {
			return s, e
		}
		base = strings.TrimSpace(string(b))
	}
	s = Snapshot{Version: 1, Repository: repo, BaseSha: base, StartSha: start, HeadSha: head, Files: []FileSnapshot{}, Limitations: []string{}, IsComplete: true}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = 4 * 1024 * 1024
	}
	raw, e := gitRead(ctx, repo, "diff", "--raw", "-z", "--no-abbrev", "--find-renames", "--no-ext-diff", "--no-textconv", base, head, "--")
	if e != nil {
		return s, e
	}
	view, objects, e := pinnedDiffView(ctx, repo, head)
	if e != nil {
		return s, e
	}
	defer os.RemoveAll(view)
	parts := bytes.Split(raw, []byte{0})
	seen := map[string]bool{}
	read := func(sha, path string, rev Revision, mode string) (*SourceSnapshot, string) {
		if mode == "000000" {
			return nil, ""
		}
		if mode == "160000" {
			return nil, "submodule objects have no source body"
		}
		spec := sha + ":" + path
		size, e := gitRead(ctx, repo, "cat-file", "-s", spec)
		if e != nil {
			return nil, "source object is unavailable"
		}
		n, e := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
		if e != nil || n > o.MaxFileBytes {
			return nil, "source object exceeds the configured byte limit"
		}
		b, e := gitRead(ctx, repo, "cat-file", "blob", spec)
		if e != nil {
			return nil, "source blob is unavailable"
		}
		if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			return nil, "binary or non-UTF-8 source"
		}
		content := string(b)
		return &SourceSnapshot{Path: path, Revision: rev, CommitSha: sha, Sha256: Hash(content), Content: content}, ""
	}
	for i := 0; i < len(parts) && len(parts[i]) > 0; i++ {
		fields := strings.Fields(string(parts[i]))
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return s, fmt.Errorf("invalid NUL-delimited Git diff")
		}
		if i+1 >= len(parts) {
			return s, fmt.Errorf("missing Git diff path")
		}
		i++
		oldPath := string(parts[i])
		newPath := oldPath
		status := fields[4]
		if status[0] == 'R' || status[0] == 'C' {
			if i+1 >= len(parts) {
				return s, fmt.Errorf("missing renamed path")
			}
			i++
			newPath = string(parts[i])
		}
		f := FileSnapshot{OldPath: oldPath, NewPath: newPath, Operation: string(status[0]), Hunks: []Hunk{}, ChangedLines: []ChangedLine{}}
		seen[oldPath] = true
		seen[newPath] = true
		var reason string
		f.Base, reason = read(base, oldPath, Base, strings.TrimPrefix(fields[0], ":"))
		if reason != "" {
			f.SkipReason = reason
		}
		f.Head, reason = read(head, newPath, Head, fields[1])
		if reason != "" {
			f.SkipReason = reason
		}
		f.IsSubmodule = strings.TrimPrefix(fields[0], ":") == "160000" || fields[1] == "160000"
		f.IsBinary = strings.Contains(f.SkipReason, "binary")
		for _, source := range []*SourceSnapshot{f.Base, f.Head} {
			if source != nil && strings.HasPrefix(source.Content, "version https://git-lfs.github.com/spec/v1\n") {
				f.IsLFS = true
				f.SkipReason = "Git LFS content is external"
			}
		}
		if f.SkipReason == "" {
			args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3", "--find-renames", base, head, "--", oldPath}
			if oldPath != newPath {
				args = append(args, newPath)
			}
			b, e := gitPinnedDiff(ctx, view, objects, head, args...)
			if e != nil {
				return s, e
			}
			f.Diff = string(b)
			f.Hunks, f.ChangedLines = parseDiff(f.Diff)
			if len(f.Hunks) == 0 && strings.Contains(f.Diff, "\nBinary files ") {
				f.IsBinary = true
				f.SkipReason = "Git classifies the source as binary"
				s.IsComplete = false
				s.Limitations = append(s.Limitations, newPath+": "+f.SkipReason)
			}
		} else {
			s.IsComplete = false
			s.Limitations = append(s.Limitations, newPath+": "+f.SkipReason)
		}
		s.Files = append(s.Files, f)
	}
	// Read object contents so later analysis does not depend on a working tree.
	for _, rev := range []Revision{Base, Head} {
		sha := base
		if rev == Head {
			sha = head
		}
		b, e := gitRead(ctx, repo, "ls-tree", "-rz", "--full-tree", sha)
		if e != nil {
			return s, e
		}
		for _, entry := range bytes.Split(b, []byte{0}) {
			pair := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(pair) != 2 {
				continue
			}
			path := string(pair[1])
			if seen[path] || !strings.HasSuffix(strings.ToLower(path), ".java") {
				continue
			}
			meta := strings.Fields(string(pair[0]))
			if len(meta) != 3 || meta[1] != "blob" {
				continue
			}
			source, reason := read(sha, path, rev, meta[0])
			if reason != "" {
				s.Limitations = append(s.Limitations, path+": dependency source unavailable: "+reason)
				continue
			}
			found := -1
			for i := range s.Files {
				if s.Files[i].Operation == "context" && s.Files[i].NewPath == path {
					found = i
					break
				}
			}
			if found < 0 {
				s.Files = append(s.Files, FileSnapshot{OldPath: path, NewPath: path, Operation: "context", Hunks: []Hunk{}, ChangedLines: []ChangedLine{}})
				found = len(s.Files) - 1
			}
			if rev == Base {
				s.Files[found].Base = source
			} else {
				s.Files[found].Head = source
			}
		}
	}
	s.ManifestSha256 = snapshotManifest(s)
	return s, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseDiff(diff string) ([]Hunk, []ChangedLine) {
	hunks := []Hunk{}
	changes := []ChangedLine{}
	oldLine, newLine := 0, 0
	active := false
	for _, line := range strings.Split(diff, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			n := func(v string) int {
				if v == "" {
					return 1
				}
				x, _ := strconv.Atoi(v)
				return x
			}
			h := Hunk{OldStart: n(m[1]), OldCount: n(m[2]), NewStart: n(m[3]), NewCount: n(m[4]), Lines: []string{}}
			hunks = append(hunks, h)
			oldLine = h.OldStart
			newLine = h.NewStart
			active = true
			continue
		}
		if !active || len(line) == 0 {
			continue
		}
		switch line[0] {
		case '-':
			changes = append(changes, ChangedLine{Base, oldLine})
			oldLine++
		case '+':
			changes = append(changes, ChangedLine{Head, newLine})
			newLine++
		case ' ':
			oldLine++
			newLine++
		default:
			continue
		}
		hunks[len(hunks)-1].Lines = append(hunks[len(hunks)-1].Lines, line)
	}
	return hunks, changes
}

package review

import (
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
)

func snapshotManifest(s Snapshot) string {
	return Hash(canonicalJSON(struct {
		BaseSha, StartSha, HeadSha string
		Files                      []FileSnapshot
	}{s.BaseSha, s.StartSha, s.HeadSha, s.Files}))
}
func ValidateSnapshot(s Snapshot) error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported native snapshot version")
	}
	for _, sha := range []string{s.BaseSha, s.StartSha, s.HeadSha} {
		if len(sha) != 40 && len(sha) != 64 {
			return fmt.Errorf("snapshot revisions must be pinned object IDs")
		}
		if _, e := hex.DecodeString(sha); e != nil {
			return fmt.Errorf("snapshot revision contains an invalid object ID")
		}
	}
	if s.ManifestSha256 == "" || s.ManifestSha256 != snapshotManifest(s) {
		return fmt.Errorf("snapshot manifest hash does not match its pinned source")
	}
	seen := map[string]bool{}
	complete := true
	for _, f := range s.Files {
		for _, p := range []string{f.OldPath, f.NewPath} {
			if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
				return fmt.Errorf("snapshot contains an invalid source path")
			}
		}
		if seen[f.NewPath] {
			return fmt.Errorf("snapshot contains duplicate file paths")
		}
		seen[f.NewPath] = true
		if !slices.Contains([]string{"A", "D", "M", "R", "C", "T", "context"}, f.Operation) {
			return fmt.Errorf("snapshot contains an unsupported file operation")
		}
		if f.SkipReason != "" {
			complete = false
		}
		for i, source := range []*SourceSnapshot{f.Base, f.Head} {
			if source == nil {
				continue
			}
			expectedPath, expectedSHA, revision := f.OldPath, s.BaseSha, Base
			if i == 1 {
				expectedPath, expectedSHA, revision = f.NewPath, s.HeadSha, Head
			}
			if source.Path != expectedPath || source.CommitSha != expectedSHA || source.Revision != revision || source.Sha256 != Hash(source.Content) {
				return fmt.Errorf("snapshot source identity or content hash does not match")
			}
		}
		parsedHunks, parsedChanges := parseDiff(f.Diff)
		if canonicalJSON(parsedHunks) != canonicalJSON(f.Hunks) || !slices.Equal(parsedChanges, f.ChangedLines) {
			return fmt.Errorf("snapshot diff metadata does not match its patch")
		}
		for _, ch := range f.ChangedLines {
			source := f.Base
			if ch.Revision == Head {
				source = f.Head
			} else if ch.Revision != Base {
				return fmt.Errorf("snapshot changed line has an invalid revision")
			}
			if source == nil || ch.Line < 1 || ch.Line > len(lines(source.Content)) {
				return fmt.Errorf("snapshot changed line is outside its pinned source")
			}
		}
		if f.SkipReason == "" {
			if (f.Operation == "A" && f.Head == nil) || (f.Operation == "D" && f.Base == nil) || (f.Operation != "A" && f.Operation != "D" && (f.Base == nil || f.Head == nil)) {
				return fmt.Errorf("snapshot source body is missing without a limitation")
			}
		}
	}
	if s.IsComplete != complete {
		return fmt.Errorf("snapshot completeness does not match its source availability")
	}
	return nil
}

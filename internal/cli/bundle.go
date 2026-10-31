package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

func executeBundle(ctx context.Context, command string, a arguments, in io.Reader, out, errOut io.Writer) int {
	fail := func(phase string, err error) int {
		diagnostic := map[string]any{"phase": phase, "status": "failed", "message": err.Error()}
		code := 5
		var resource *runstore.ContentLimitError
		var artifact *runstore.ArtifactSizeError
		if errors.As(err, &resource) {
			diagnostic["code"] = "REVIEW_BUNDLE_RESOURCE_LIMIT"
			diagnostic["resource"] = resource.Resource
			diagnostic["actual"] = resource.Actual
			diagnostic["allowed"] = resource.Allowed
		}
		if errors.As(err, &artifact) {
			diagnostic["code"] = "REVIEW_ARTIFACT_SIZE_LIMIT"
			diagnostic["artifact"] = artifact.Artifact
			diagnostic["actual"] = artifact.Actual
			diagnostic["allowed"] = artifact.Allowed
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = 124
			diagnostic["code"] = "REVIEW_DEADLINE_EXCEEDED"
		} else if errors.Is(ctx.Err(), context.Canceled) {
			code = 130
			diagnostic["code"] = "REVIEW_CANCELLED"
		}
		_ = json.NewEncoder(errOut).Encode(diagnostic)
		return code
	}
	if err := ctx.Err(); err != nil {
		return fail("start", err)
	}
	if command != "review" && command != "archive" {
		return fail("arguments", errors.New("--artifacts-dir requires review or archive"))
	}
	if a.exportDir != "" && command != "archive" {
		return fail("arguments", errors.New("--export-dir requires archive --artifacts-dir"))
	}
	if a.runDir != "" || a.format != "native" || a.eventsFile != "" || a.input != "" || a.apiKey != "" {
		return fail("arguments", errors.New("bundle review fixes its native run directory and requires stored credential references"))
	}
	if command == "archive" {
		bundle, err := engine.OpenBundle(a.artifactsDir, "")
		if err != nil {
			return fail("archive", err)
		}
		lease, err := runstore.Open(bundle.Directory, bundle.Manifest.InvocationSha256)
		if err != nil {
			return fail("archive", err)
		}
		defer lease.Close()
		plan, err := bundle.LoadPlanContext(ctx)
		if err != nil {
			return fail("archive", err)
		}
		var archive engine.BundleArchive
		if _, e := os.Lstat(filepath.Join(bundle.Directory, "execution", "state.json")); errors.Is(e, os.ErrNotExist) {
			archive, err = bundle.PublicArchive(plan)
		} else if e != nil {
			err = e
		} else {
			archive, err = bundle.ExportArchive(plan, filepath.Join(bundle.Directory, "execution"))
		}
		if err != nil {
			return fail("archive", err)
		}
		exporter := engine.Identity()
		bundle.Manifest.Exporter = &exporter
		if err = bundle.SaveManifestContext(ctx); err != nil {
			return fail("archive", err)
		}
		if a.output != "" && a.output != "-" {
			if err = validateBundleOutput(bundle.Directory, a.output); err != nil {
				return fail("archive", err)
			}
			if err = runstore.AtomicJSON(a.output, archive); err != nil {
				return fail("archive", err)
			}
		}
		if a.exportDir != "" {
			if err = bundle.ExportPublic(a.exportDir); err != nil {
				return fail("export", err)
			}
		}
		return outputValue(bundle.Manifest, "-", out, errOut)
	}
	if a.baseSHA == "" || a.startSHA == "" || a.headSHA == "" {
		return fail("arguments", errors.New("bundle review requires --base-sha, --start-sha and --head-sha"))
	}
	if a.failOn != "" {
		return fail("arguments", errors.New("bundle review reports findings independently of its execution status"))
	}
	policyLimits := engine.BundleResourcePolicy{MaximumUniqueBytes: a.maxBundleUniqueBytes, MaximumParts: a.maxBundleParts, MaximumPartBytes: runstore.MaximumContentPartBytes, DeadlineSeconds: a.deadlineSeconds}
	cfg, route, err := routing(a)
	if err != nil {
		return fail("configuration", err)
	}
	read := func(path string) (json.RawMessage, error) {
		if path == "-" {
			data, err := io.ReadAll(io.LimitReader(in, runstore.MaximumDocumentBytes+1))
			if len(data) > runstore.MaximumDocumentBytes {
				return nil, errors.New("input exceeds its byte limit")
			}
			return data, err
		}
		return runstore.ReadBoundedFile(path, runstore.MaximumDocumentBytes)
	}
	prompt, err := loadPrompt(a, read)
	if err != nil {
		return fail("configuration", err)
	}
	var task json.RawMessage
	if a.taskFile != "" {
		task, err = read(a.taskFile)
		if err != nil || !json.Valid(task) {
			return fail("configuration", errors.New("task must be valid JSON"))
		}
	}
	repository, err := filepath.Abs(a.repo)
	if err != nil {
		return fail("configuration", err)
	}
	repository, err = filepath.EvalSymlinks(repository)
	if err != nil {
		return fail("configuration", err)
	}
	fingerprint, err := json.Marshal(struct {
		Version        int                         `json:"version"`
		Repository     string                      `json:"repository"`
		BaseSha        string                      `json:"baseSha"`
		StartSha       string                      `json:"startSha"`
		HeadSha        string                      `json:"headSha"`
		PolicySha256   string                      `json:"policySha256"`
		TaskSha256     string                      `json:"taskSha256"`
		Route          any                         `json:"route"`
		ResourcePolicy engine.BundleResourcePolicy `json:"resourcePolicy"`
	}{2, repository, a.baseSHA, a.startSHA, a.headSHA, review.Hash(prompt), review.Hash(string(task)), route, policyLimits})
	if err != nil {
		return fail("configuration", err)
	}
	identity := review.Hash(string(fingerprint))
	bundle, err := engine.OpenBundleWithPolicy(a.artifactsDir, identity, policyLimits)
	if err != nil {
		return fail("identity", err)
	}
	lease, err := runstore.Open(bundle.Directory, identity)
	if err != nil {
		return fail("lease", err)
	}
	defer lease.Close()
	// Check again after the lease so two starts cannot publish different plans.
	bundle, err = engine.OpenBundleWithPolicy(a.artifactsDir, identity, policyLimits)
	if err != nil {
		return fail("identity", err)
	}
	if bundle.Manifest.BaseSha == "" {
		bundle.Manifest.BaseSha, bundle.Manifest.StartSha, bundle.Manifest.HeadSha = a.baseSHA, a.startSHA, a.headSHA
	} else if bundle.Manifest.BaseSha != a.baseSHA || bundle.Manifest.StartSha != a.startSHA || bundle.Manifest.HeadSha != a.headSHA {
		return fail("identity", errors.New("saved source does not match the pinned invocation"))
	}
	if err = bundle.SaveManifestContext(ctx); err != nil {
		return fail("identity", err)
	}
	inputPath := filepath.Join(bundle.Directory, "invocation.json")
	if saved, e := runstore.ReadPrivate(inputPath, runstore.MaximumDocumentBytes); e == nil {
		if string(saved) != string(fingerprint) {
			return fail("identity", errors.New("saved invocation input changed"))
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fail("identity", e)
	} else if err = runstore.AtomicBytesContext(ctx, inputPath, fingerprint); err != nil {
		return fail("identity", err)
	}
	if bundle.Manifest.BaseSha != "" && (bundle.Manifest.BaseSha != a.baseSHA || bundle.Manifest.StartSha != a.startSHA || bundle.Manifest.HeadSha != a.headSHA) {
		return fail("identity", errors.New("saved source does not match the pinned invocation"))
	}
	if _, ok := bundle.Manifest.Entries["plan"]; ok {
		if _, source := bundle.Manifest.Entries["snapshot"]; !source {
			return fail("snapshot", errors.New("saved plan has no source snapshot"))
		}
	}
	ctx, cancel := context.WithDeadline(ctx, bundle.Manifest.StartedAt.Add(time.Duration(bundle.Manifest.DeadlineSeconds)*time.Second))
	defer cancel()
	for _, path := range []string{a.output, a.archiveOutput} {
		if err = validateBundleOutput(bundle.Directory, path); err != nil {
			return fail("arguments", err)
		}
	}
	started := time.Now()
	phase := func(name string, operation func() error) error {
		at := time.Now()
		before := cpuMilliseconds()
		_ = json.NewEncoder(errOut).Encode(map[string]any{"phase": name, "status": "started", "startedAt": at.UTC()})
		err := ctx.Err()
		if err == nil {
			err = operation()
		}
		if err == nil {
			err = ctx.Err()
		}
		status := "completed"
		if err != nil {
			status = "failed"
		}
		_ = json.NewEncoder(errOut).Encode(map[string]any{"phase": name, "status": status, "elapsedMilliseconds": time.Since(at).Milliseconds(), "cpuMilliseconds": cpuMilliseconds() - before, "totalElapsedMilliseconds": time.Since(started).Milliseconds()})
		return err
	}
	var plan engine.Plan
	if _, ok := bundle.Manifest.Entries["plan"]; !ok {
		if _, err = bundle.RecoverPlanMetadataContext(ctx); err != nil {
			return fail("plan", err)
		}
	}
	if _, ok := bundle.Manifest.Entries["plan"]; ok {
		if err = phase("resume", func() error { var e error; plan, e = bundle.LoadPlanContext(ctx); return e }); err != nil {
			return fail("resume", err)
		}
	} else {
		if _, err = os.Lstat(filepath.Join(bundle.Directory, "execution")); err == nil {
			return fail("identity", errors.New("saved execution has no committed plan; it must not be rebuilt"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail("identity", err)
		}
		var snapshot engine.Snapshot
		if _, ok := bundle.Manifest.Entries["snapshot"]; ok {
			if err = phase("snapshot", func() error { var e error; snapshot, e = bundle.SnapshotContext(ctx); return e }); err != nil {
				return fail("snapshot", err)
			}
		} else {
			err = phase("prepare", func() error {
				source, e := review.Prepare(ctx, review.PrepareOptions{Repository: repository, Base: a.baseSHA, Start: a.startSHA, Head: a.headSHA, ExactBase: true})
				if e != nil {
					return e
				}
				if e = ctx.Err(); e != nil {
					return e
				}
				snapshot = engine.WrapSnapshot(source, task)
				return bundle.SaveSnapshotContext(ctx, snapshot)
			})
			if err != nil {
				return fail("prepare", err)
			}
		}
		var document review.Document
		if _, ok := bundle.Manifest.Entries["document"]; ok {
			err = phase("analysis-resume", func() error { saved, e := bundle.SharedDocumentContext(ctx); document = saved.Document; return e })
		} else {
			if bundle.Manifest.SelectionVersion != review.CurrentSelectionVersion {
				return fail("analysis", errors.New("saved selection policy requires its original analyzer; a new policy must not rebuild this invocation"))
			}
			err = phase("analysis", func() error { var e error; document, e = review.AnalyzeContext(ctx, snapshot.Snapshot); return e })
		}
		if err != nil {
			return fail("analysis", err)
		}
		err = phase("plan", func() error {
			var e error
			plan, e = engine.BuildDocumentContext(ctx, document, route, prompt, snapshot.Task)
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			plan.DeadlineSeconds = policyLimits.DeadlineSeconds
			plan.RunID, plan.PassID = bundle.Manifest.RunID, bundle.Manifest.PassID
			return bundle.SavePlanContext(ctx, plan)
		})
		if err != nil {
			return fail("plan", err)
		}
		plan, err = bundle.LoadPlanContext(ctx)
		if err != nil {
			return fail("plan", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return fail("plan", err)
	}
	plan.Route.RuntimeSecrets = route.RuntimeSecrets
	executionDir := filepath.Join(bundle.Directory, "execution")
	var result engine.Result
	if _, e := os.Lstat(filepath.Join(executionDir, "state.json")); e == nil {
		resume, e := bundle.CanResumeContext(ctx, plan, executionDir)
		if e != nil {
			return fail("recovery", e)
		}
		if !resume {
			archive, e := bundle.ExportArchive(plan, executionDir)
			if e != nil {
				return fail("recovery", e)
			}
			return finishBundle(bundle, archive.Status, a, out, errOut)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fail("recovery", e)
	} else if _, ok := bundle.Manifest.Entries["archive"]; ok {
		archive, e := bundle.PublicArchive(plan)
		if e != nil {
			return fail("recovery", e)
		}
		return finishBundle(bundle, archive.Status, a, out, errOut)
	} else if bundle.Manifest.Status == "running" || bundle.Manifest.Status == "unknown" || bundle.Manifest.Entries["result"].Sha256 != "" {
		bundle.Manifest.Status = "unknown"
		if e = bundle.SaveManifestContext(ctx); e != nil {
			return fail("recovery", e)
		}
		return fail("recovery", errors.New("saved inference fence has no journal; its outcome is unknown and it must not be repeated"))
	}
	if err = ctx.Err(); err != nil {
		return fail("checkpoint", err)
	}
	bundle.Manifest.Status = "running"
	if err = bundle.SaveManifestContext(ctx); err != nil {
		return fail("checkpoint", err)
	}
	var runErr error
	err = phase("run", func() error { result, runErr = engine.Run(ctx, plan, cfg, executionDir); return runErr })
	if result.Kind == "" {
		if err == nil {
			err = errors.New("review returned no native result")
		}
		return fail("run", err)
	}
	if err = bundle.SaveResult(result); err != nil {
		return fail("result", err)
	}
	archive, err := bundle.ExportArchive(plan, executionDir)
	if err != nil {
		return fail("archive", err)
	}
	if err = ctx.Err(); err != nil {
		return fail("run", err)
	}
	return finishBundle(bundle, archive.Status, a, out, errOut)
}

func finishBundle(bundle *engine.BundleStore, status string, a arguments, out, errOut io.Writer) int {
	if a.output != "" && a.output != "-" {
		value := map[string]any{"schemaVersion": 2, "kind": "ReviewResultReference", "runId": bundle.Manifest.RunID, "passId": bundle.Manifest.PassID, "status": status, "resultRef": bundle.Manifest.Entries["result"]}
		if err := runstore.AtomicJSON(a.output, value); err != nil {
			fmt.Fprintln(errOut, err)
			return 5
		}
	}
	if a.archiveOutput != "" {
		var archive engine.BundleArchive
		if err := bundle.Content.Decode(bundle.Manifest.Entries["archive"], &archive); err != nil {
			fmt.Fprintln(errOut, err)
			return 5
		}
		if err := runstore.AtomicJSON(a.archiveOutput, archive); err != nil {
			fmt.Fprintln(errOut, err)
			return 5
		}
	}
	if code := outputValue(bundle.Manifest, "-", out, errOut); code != 0 {
		return code
	}
	switch status {
	case "complete":
		return 0
	case "partial":
		return 3
	case "unknown":
		return 4
	default:
		return 4
	}
}

func validateBundleOutput(directory, path string) error {
	if path == "" || path == "-" {
		return nil
	}
	resolved, err := physicalArchivePath(path)
	if err != nil {
		return err
	}
	for _, name := range []string{"bundle.json", "plan.json", "invocation.json", "writer.lock", "content", "execution"} {
		protected := filepath.Join(directory, name)
		if resolved == protected || name == "content" && withinDirectory(protected, resolved) || name == "execution" && withinDirectory(protected, resolved) {
			return errors.New("bundle output aliases protected input or state")
		}
	}
	return nil
}

func withinDirectory(directory, path string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func cpuMilliseconds() int64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return usage.Utime.Sec*1000 + int64(usage.Utime.Usec)/1000 + usage.Stime.Sec*1000 + int64(usage.Stime.Usec)/1000
}

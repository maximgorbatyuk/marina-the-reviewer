package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

type arguments struct {
	input, output, document, modelOutput, format                                  string
	repo, base, start, head, baseSHA, startSHA, headSHA                           string
	config, runDir, eventsFile, archiveOutput, systemPrompt, promptFile, taskFile string
	profile, fallback, baseURL, model, apiKey, apiKeyEnv, apiKeyFile              string
	runID, passID, clusterID, failOn                                              string
	noFallback, json                                                              bool
	contextLength, maxOutput, batchIndex                                          int
	maxBytes                                                                      int64
}

func execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	command := args[0]
	rest := args[1:]
	if command == "capabilities" {
		return outputValue(map[string]any{"tool": "marina-the-reviewer", "command": "marinadoreview", "schemaVersion": 1, "protocolVersion": 3, "validationPolicyVersion": 2, "baselineCommit": engine.Baseline, "executionArchiveVersion": engine.ExecutionArchiveVersion, "backends": []string{"openai-compatible"}, "primaryFallback": true, "javaSemanticAnalysis": true, "nonJavaFallback": true, "resume": true}, "-", out, errOut)
	}
	if command == "config" {
		if len(rest) == 0 || rest[0] != "init" {
			fmt.Fprintln(errOut, "usage: marinadoreview config init [--config PATH]")
			return 2
		}
		command = "config-init"
		rest = rest[1:]
	}
	if command == "prompts" {
		if len(rest) == 0 || rest[0] != "show" {
			fmt.Fprintln(errOut, "usage: marinadoreview prompts show --builtin review")
			return 2
		}
		fs := flag.NewFlagSet("prompts show", flag.ContinueOnError)
		fs.SetOutput(errOut)
		builtin := fs.String("builtin", "review", "built-in prompt name")
		if fs.Parse(rest[1:]) != nil || *builtin != "review" || fs.NArg() != 0 {
			return 2
		}
		_, err := fmt.Fprintln(out, review.DefaultSystemPrompt())
		if err != nil {
			return 5
		}
		return 0
	}
	if command == "compat" {
		if len(rest) == 0 || (rest[0] != "import" && rest[0] != "export") {
			fmt.Fprintln(errOut, "usage: marinadoreview compat import|export --input PATH")
			return 2
		}
		command = "compat-" + rest[0]
		rest = rest[1:]
	}
	switch command {
	case "prepare", "plan", "run", "review", "validate", "archive", "compat-import", "compat-export", "config-init":
	default:
		fmt.Fprintf(errOut, "unknown command: %s\n", command)
		return 2
	}
	a, err := parseArguments(command, rest, errOut)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if err = validateArchivePaths(command, a); err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if command == "archive" {
		archive, err := engine.ExportArchive(a.runDir)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 5
		}
		if a.output != "" && a.output != "-" {
			if err = archive.Write(a.output); err != nil {
				fmt.Fprintln(errOut, "output could not be written")
				return 5
			}
			return 0
		}
		return outputValue(archive, a.output, out, errOut)
	}
	if a.failOn != "" && a.failOn != "warning" && a.failOn != "error" && a.failOn != "critical" {
		fmt.Fprintln(errOut, "--fail-on must be warning, error or critical")
		return 2
	}
	if command == "run" && (a.profile != "" || a.fallback != "" || a.noFallback || a.baseURL != "" || a.model != "" || a.contextLength != 0 || a.maxOutput != 0 || a.systemPrompt != "" || a.promptFile != "" || a.taskFile != "") {
		fmt.Fprintln(errOut, "run uses a fixed plan; use plan to change providers, routing, prompt or budgets")
		return 2
	}
	stdinUsers := 0
	for _, p := range []string{a.input, a.document, a.modelOutput, a.promptFile, a.taskFile, a.apiKeyFile} {
		if p == "-" {
			stdinUsers++
		}
	}
	if stdinUsers > 1 || a.apiKeyFile == "-" {
		fmt.Fprintln(errOut, "stdin can supply only one JSON or prompt document; use a private token file or environment variable")
		return 2
	}
	if a.format != "native" && a.format != "marina-v3" {
		fmt.Fprintln(errOut, "unsupported output format")
		return 2
	}
	if a.format == "marina-v3" && (command == "prepare" || command == "config-init" || command == "compat-import") {
		fmt.Fprintln(errOut, "this command creates a native document; export a plan or result for Marina")
		return 2
	}
	if a.batchIndex < 0 || a.eventsFile == "-" {
		fmt.Fprintln(errOut, "invalid batch index or event output path")
		return 2
	}
	if a.eventsFile != "" && a.output != "-" {
		left, _ := filepath.Abs(a.eventsFile)
		right, _ := filepath.Abs(a.output)
		if left == right {
			fmt.Fprintln(errOut, "result and events require separate files")
			return 2
		}
	}
	if command == "config-init" {
		path, e := configPath(a.config)
		if e == nil {
			e = config.Init(path)
		}
		if e != nil {
			fmt.Fprintln(errOut, e)
			return 2
		}
		return outputValue(map[string]string{"config": path}, a.output, out, errOut)
	}
	read := func(path string) (json.RawMessage, error) {
		if path == "" {
			return nil, errors.New("input path is required")
		}
		var b []byte
		var e error
		if path == "-" {
			b, e = io.ReadAll(io.LimitReader(in, a.maxBytes+1))
			if int64(len(b)) > a.maxBytes {
				return nil, errors.New("input exceeds the size limit")
			}
		} else {
			b, e = runstore.ReadBoundedFile(path, a.maxBytes)
		}
		if e != nil {
			return nil, e
		}
		return b, nil
	}
	fail := func(e error, code int) int {
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "review canceled")
			return 130
		}
		fmt.Fprintln(errOut, e)
		return code
	}
	var value any
	switch command {
	case "prepare", "review":
		if (a.baseSHA != "" || a.startSHA != "" || a.headSHA != "") && (a.baseSHA == "" || a.startSHA == "" || a.headSHA == "") {
			return fail(errors.New("exact replay requires --base-sha, --start-sha and --head-sha"), 2)
		}
		opts := review.PrepareOptions{Repository: a.repo, Base: a.base, Start: a.start, Head: a.head}
		if a.baseSHA != "" {
			opts.Base = a.baseSHA
			opts.Start = a.startSHA
			opts.Head = a.headSHA
			opts.ExactBase = true
		}
		snap, e := review.Prepare(ctx, opts)
		if e != nil {
			return fail(e, 5)
		}
		var task json.RawMessage
		if a.taskFile != "" {
			task, e = read(a.taskFile)
			if e != nil || !json.Valid(task) {
				return fail(errors.New("task must be valid JSON"), 2)
			}
		}
		snapshot := engine.WrapSnapshot(snap, task)
		if command == "prepare" {
			value = snapshot
			break
		}
		cfg, route, e := routing(a)
		if e != nil {
			return fail(e, 2)
		}
		prompt, e := loadPrompt(a, read)
		if e != nil {
			return fail(e, 2)
		}
		plan, e := engine.Build(snapshot, route, prompt)
		if e != nil {
			return fail(e, 5)
		}
		return executePlan(ctx, plan, cfg, a, out, errOut)
	case "plan":
		raw, e := read(a.input)
		if e != nil {
			return fail(e, 5)
		}
		cfg, route, e := routing(a)
		_ = cfg
		if e != nil {
			return fail(e, 2)
		}
		if route.Primary.Auth.Kind == "ephemeral" {
			return fail(errors.New("saved plans require --api-key-env or --api-key-file; use review for a one-time --api-key"), 2)
		}
		prompt, e := loadPrompt(a, read)
		if e != nil {
			return fail(e, 2)
		}
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return fail(errors.New("invalid input JSON"), 5)
		}
		var plan engine.Plan
		switch header.Kind {
		case "ReviewSnapshot":
			var s engine.Snapshot
			if json.Unmarshal(raw, &s) != nil {
				return fail(errors.New("invalid snapshot"), 5)
			}
			plan, e = engine.Build(s, route, prompt)
		case "ReviewPlan":
			var old engine.Plan
			if json.Unmarshal(raw, &old) != nil || old.SchemaVersion != 1 || old.Compatibility != engine.Contract() {
				return fail(errors.New("invalid plan"), 5)
			}
			var task json.RawMessage
			if len(old.Prepared.Batches) > 0 {
				task = old.Prepared.Batches[0].Input.Task
			}
			if prompt == "" && len(old.Prepared.Batches) > 0 {
				prompt = old.Prepared.Batches[0].SystemPrompt
			}
			plan, e = engine.BuildDocument(old.Document, route, prompt, task)
		case "ReviewDocument":
			var doc engine.Document
			if json.Unmarshal(raw, &doc) != nil || doc.SchemaVersion != 1 || doc.Compatibility != engine.Contract() {
				return fail(errors.New("invalid review document"), 5)
			}
			plan, e = engine.BuildDocument(doc.Document, route, prompt, doc.Task)
		default:
			return fail(errors.New("plan requires a native snapshot, document or plan"), 5)
		}
		if e != nil {
			return fail(e, 5)
		}
		value = plan
		if a.format == "marina-v3" {
			if a.batchIndex > 0 {
				if a.batchIndex > len(plan.Prepared.Batches) {
					return fail(errors.New("batch index exceeds plan"), 2)
				}
				value = plan.Prepared.Batches[a.batchIndex-1]
			} else {
				value = plan.Prepared
			}
		}
	case "run":
		raw, e := read(a.input)
		if e != nil {
			return fail(e, 5)
		}
		var p engine.Plan
		if json.Unmarshal(raw, &p) != nil {
			return fail(errors.New("invalid plan JSON"), 5)
		}
		cfg, e := loadConfig(a.config)
		if e != nil {
			return fail(e, 2)
		}
		if a.apiKey != "" || a.apiKeyEnv != "" || a.apiKeyFile != "" {
			count := 0
			for _, s := range []string{a.apiKey, a.apiKeyEnv, a.apiKeyFile} {
				if s != "" {
					count++
				}
			}
			if count != 1 {
				return fail(errors.New("use one credential override"), 2)
			}
			token := a.apiKey
			if token == "" {
				ref := config.SecretSource{Kind: "env", Name: a.apiKeyEnv}
				if a.apiKeyFile != "" {
					ref = config.SecretSource{Kind: "file", Name: a.apiKeyFile}
				}
				token, e = config.ResolveToken(config.ProviderSnapshot{Auth: ref}, cfg, nil)
				if e != nil {
					return fail(e, 2)
				}
			}
			p.Route.RuntimeSecrets = map[string]string{p.Route.Primary.Name: token}
		}
		return executePlan(ctx, p, cfg, a, out, errOut)
	case "validate":
		raw, e := read(a.input)
		if e != nil {
			return fail(e, 5)
		}
		var shared json.RawMessage
		if a.document != "" {
			shared, e = read(a.document)
			if e != nil {
				return fail(e, 5)
			}
		}
		batch, e := review.ImportPreparedBatch(raw, shared)
		if e != nil {
			return fail(e, 5)
		}
		model, e := read(a.modelOutput)
		if e != nil {
			return fail(e, 5)
		}
		model, e = unwrapModel(model)
		if e != nil {
			return fail(e, 5)
		}
		result, e := review.Validate(batch, model, review.Identity{RunID: a.runID, PassID: a.passID, ClusterID: a.clusterID, PolicyVersion: 2})
		if e != nil {
			return fail(e, 5)
		}
		value = result
		if a.format == "native" {
			status := "complete"
			if !result.IsComplete {
				status = "partial"
			}
			value = engine.Result{SchemaVersion: 1, Kind: "ReviewResult", Producer: engine.Identity(), Compatibility: engine.Contract(), RunID: a.runID, PassID: a.passID, Status: status, Result: result, Attempts: []engine.Attempt{}}
		}
	case "compat-import":
		raw, e := read(a.input)
		if e != nil {
			return fail(e, 5)
		}
		if artifact, ok, e := importArtifact(raw); e != nil {
			return fail(e, 5)
		} else if ok {
			value = artifact
			break
		}
		var shared json.RawMessage
		if a.document != "" {
			shared, e = read(a.document)
			if e != nil {
				return fail(e, 5)
			}
		}
		var bundle struct{ Batch, Plan, Document json.RawMessage }
		if json.Unmarshal(raw, &bundle) == nil && len(bundle.Document) > 0 {
			shared = bundle.Document
			if len(bundle.Batch) > 0 {
				raw = bundle.Batch
			} else if len(bundle.Plan) > 0 {
				raw = bundle.Plan
			}
		}
		prepared, e := review.ImportPreparedPlan(raw, shared)
		if e != nil || prepared.Batches == nil {
			b, be := review.ImportPreparedBatch(raw, shared)
			if be != nil {
				return fail(be, 5)
			}
			prepared = review.PreparedPlan{Version: 3, Purpose: review.ArchitectureReview, GraphSha256: b.Input.Graph.Sha256, Batches: []review.PreparedBatch{b}, Limitations: b.Input.Limitations, IsComplete: b.Input.Batch.IsWholeReview}
		}
		doc, task, e := documentFromPrepared(prepared)
		if e != nil {
			return fail(e, 5)
		}
		value = engine.Document{SchemaVersion: 1, Kind: "ReviewDocument", Producer: engine.Identity(), Compatibility: engine.Contract(), Document: doc, Task: task}
	case "compat-export":
		raw, e := read(a.input)
		if e != nil {
			return fail(e, 5)
		}
		value, e = exportNative(raw, a.batchIndex)
		if e != nil {
			return fail(e, 5)
		}
	}
	return outputValue(value, a.output, out, errOut)
}

func parseArguments(command string, args []string, errOut io.Writer) (arguments, error) {
	a := arguments{}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errOut)
	f.StringVar(&a.input, "input", "", "input JSON path or -")
	f.StringVar(&a.output, "output", "-", "output JSON path or -")
	f.StringVar(&a.document, "document", "", "shared Marina document JSON")
	f.StringVar(&a.modelOutput, "model-output", "", "model JSON or output archive")
	f.StringVar(&a.format, "format", "native", "native or marina-v3")
	f.StringVar(&a.repo, "repo", ".", "repository path")
	f.StringVar(&a.base, "base", "origin/dev", "target Git revision")
	f.StringVar(&a.start, "start", "", "start revision")
	f.StringVar(&a.head, "head", "HEAD", "head Git revision")
	f.StringVar(&a.baseSHA, "base-sha", "", "exact base commit")
	f.StringVar(&a.startSHA, "start-sha", "", "exact start commit")
	f.StringVar(&a.headSHA, "head-sha", "", "exact head commit")
	f.StringVar(&a.config, "config", "", "provider configuration path")
	f.StringVar(&a.runDir, "run-dir", "", "private resume directory")
	f.StringVar(&a.eventsFile, "events-file", "", "separate JSONL events path")
	f.StringVar(&a.archiveOutput, "archive-output", "", "private execution archive JSON path")
	f.StringVar(&a.systemPrompt, "system-prompt", "", "review policy override")
	f.StringVar(&a.promptFile, "system-prompt-file", "", "review policy file")
	f.StringVar(&a.taskFile, "task-file", "", "task context JSON file")
	f.StringVar(&a.profile, "profile", "", "primary profile")
	f.StringVar(&a.fallback, "fallback-profile", "", "fallback profile")
	f.BoolVar(&a.noFallback, "no-fallback", false, "disable fallback")
	f.StringVar(&a.baseURL, "base-url", "", "primary API URL override")
	f.StringVar(&a.model, "model", "", "primary model override")
	f.StringVar(&a.apiKey, "api-key", "", "one-time primary API token")
	f.StringVar(&a.apiKeyEnv, "api-key-env", "", "primary API token environment variable")
	f.StringVar(&a.apiKeyFile, "api-key-file", "", "private primary API token file")
	f.IntVar(&a.batchIndex, "batch", 0, "one-based prepared batch index for Marina export")
	f.IntVar(&a.contextLength, "context-length", 0, "primary context limit")
	f.IntVar(&a.maxOutput, "max-output-tokens", 0, "primary output limit")
	f.StringVar(&a.runID, "run-id", "", "Marina run UUID")
	f.StringVar(&a.passID, "pass-id", "", "Marina pass UUID")
	f.StringVar(&a.clusterID, "cluster-id", "", "Marina cluster UUID")
	f.StringVar(&a.failOn, "fail-on", "", "CI finding threshold: warning, error or critical")
	f.BoolVar(&a.json, "json", false, "JSON output")
	f.Int64Var(&a.maxBytes, "max-document-bytes", runstore.MaximumDocumentBytes, "maximum input document size")
	if e := f.Parse(args); e != nil {
		return a, e
	}
	if f.NArg() != 0 || a.maxBytes < 1 || a.maxBytes > runstore.MaximumDocumentBytes {
		fmt.Fprintln(errOut, "invalid positional argument or document byte limit")
		return a, errors.New("invalid arguments")
	}
	return a, nil
}

func outputValue(value any, path string, out, errOut io.Writer) int {
	var err error
	if path != "-" && path != "" {
		err = runstore.AtomicJSON(path, value)
	} else {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		err = encoder.Encode(value)
	}
	if err != nil {
		fmt.Fprintln(errOut, "output could not be written")
		return 5
	}
	return 0
}

func configPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	return config.DefaultPath()
}
func loadConfig(path string) (config.Config, error) {
	p, e := configPath(path)
	if e != nil {
		return config.Config{}, e
	}
	c, e := config.Load(p)
	if path == "" && errors.Is(e, os.ErrNotExist) {
		return config.Config{}, nil
	}
	return c, e
}
func routing(a arguments) (config.Config, config.Route, error) {
	c, e := loadConfig(a.config)
	if e != nil {
		return c, config.Route{}, e
	}
	if c.Version == 0 {
		if a.baseURL == "" || a.model == "" {
			return c, config.Route{}, errors.New("configure a provider or supply --base-url and --model")
		}
		c = config.Config{Version: 1, Routing: config.Routing{Primary: "request"}, Profiles: map[string]config.Profile{"request": {BaseURL: a.baseURL, Model: a.model, ContextLength: 131072}}}
	}
	r, e := config.ResolveRouting(c, config.Overrides{Primary: a.profile, Fallback: a.fallback, NoFallback: a.noFallback, BaseURL: a.baseURL, Model: a.model, APIKey: a.apiKey, APIKeyEnv: a.apiKeyEnv, APIKeyFile: a.apiKeyFile, ContextLength: a.contextLength, MaximumOutputTokens: a.maxOutput})
	return c, r, e
}
func loadPrompt(a arguments, read func(string) (json.RawMessage, error)) (string, error) {
	if a.systemPrompt != "" && a.promptFile != "" {
		return "", errors.New("use one system prompt override")
	}
	if a.promptFile != "" {
		b, e := read(a.promptFile)
		return string(b), e
	}
	return a.systemPrompt, nil
}

func executePlan(ctx context.Context, p engine.Plan, c config.Config, a arguments, out, errOut io.Writer) int {
	dir := a.runDir
	if dir == "" {
		dir = filepath.Join("review-run", p.RunID)
	}
	a.runDir = dir
	if err := validateArchivePaths("run", a); err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		fmt.Fprintln(errOut, "run directory could not be created")
		return 5
	}
	result, err := engine.Run(ctx, p, c, dir)
	if result.Kind == "" {
		fmt.Fprintln(errOut, err)
		var configurationError *engine.ConfigurationError
		if errors.As(err, &configurationError) {
			return 2
		}
		return 5
	}
	var wire any = result
	if a.format == "marina-v3" {
		wire = result.Result
	}
	if code := outputValue(wire, a.output, out, errOut); code != 0 {
		return code
	}
	if a.archiveOutput != "" {
		archive, archiveErr := engine.ExportArchive(dir)
		if archiveErr == nil {
			archiveErr = archive.Write(a.archiveOutput)
		}
		if archiveErr != nil {
			fmt.Fprintln(errOut, "execution archive could not be written:", archiveErr)
			return 5
		}
	}
	if a.eventsFile != "" {
		data, e := runstore.ReadPrivate(filepath.Join(dir, "events.jsonl"), runstore.MaximumDocumentBytes)
		if errors.Is(e, os.ErrNotExist) {
			data = nil
			e = nil
		}
		if e == nil {
			e = writeEvents(a.eventsFile, data)
		}
		if e != nil {
			fmt.Fprintln(errOut, "events could not be written")
			return 5
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(errOut, "review canceled")
		return 130
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		if result.Status == "failed" {
			return 4
		}
		return 3
	}
	threshold := map[string]int{"warning": 1, "error": 2, "critical": 3}[a.failOn]
	if threshold > 0 {
		for _, f := range result.Result.Findings {
			if f.Severity >= threshold {
				return 10
			}
		}
	}
	return 0
}

func writeEvents(path string, data []byte) error {
	if path == "-" {
		return errors.New("events require a separate file")
	}
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return errors.New("events output is not a regular file")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".marina-events-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}

func documentFromPrepared(p review.PreparedPlan) (review.Document, json.RawMessage, error) {
	if len(p.Batches) == 0 {
		return review.Document{}, nil, errors.New("prepared review has no source batches")
	}
	b := p.Batches[0].Input
	d := review.Document{Version: 3, ManifestSha256: b.ManifestSha256, BaseSha: b.BaseSha, StartSha: b.StartSha, HeadSha: b.HeadSha, Graph: b.Graph, Blocks: []review.Block{}, Coverage: review.Coverage{IsComplete: p.IsComplete}, Limitations: p.Limitations}
	seen := map[string]bool{}
	for _, batch := range p.Batches {
		if batch.Input.Graph.Sha256 != d.Graph.Sha256 || batch.Input.ManifestSha256 != d.ManifestSha256 {
			return d, nil, errors.New("prepared batches have different source identities")
		}
		for _, block := range batch.Input.Batch.Blocks {
			if seen[block.ID] {
				return d, nil, errors.New("prepared batches contain duplicate block IDs")
			}
			seen[block.ID] = true
			d.Blocks = append(d.Blocks, block)
		}
	}
	return d, b.Task, nil
}

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

const ReviewArtifactBundleVersion = 2

type ArtifactBundle struct {
	SchemaVersion    int                            `json:"schemaVersion"`
	Kind             string                         `json:"kind"`
	Producer         Producer                       `json:"producer"`
	Compatibility    Compatibility                  `json:"compatibility"`
	InvocationSha256 string                         `json:"invocationSha256"`
	RunID            string                         `json:"runId"`
	PassID           string                         `json:"passId"`
	BaseSha          string                         `json:"baseSha"`
	StartSha         string                         `json:"startSha"`
	HeadSha          string                         `json:"headSha"`
	SelectionVersion int                            `json:"selectionVersion"`
	PlanHash         string                         `json:"planHash"`
	PlanFileSha256   string                         `json:"planFileSha256"`
	Status           string                         `json:"status"`
	StartedAt        time.Time                      `json:"startedAt"`
	DeadlineSeconds  int                            `json:"deadlineSeconds"`
	ResourcePolicy   BundleResourcePolicy           `json:"resourcePolicy"`
	Exporter         *Producer                      `json:"exporter,omitempty"`
	Entries          map[string]runstore.ContentRef `json:"entries"`
}

type BundlePlan struct {
	SchemaVersion   int                 `json:"schemaVersion"`
	Kind            string              `json:"kind"`
	Producer        Producer            `json:"producer"`
	Compatibility   Compatibility       `json:"compatibility"`
	RunID           string              `json:"runId"`
	PassID          string              `json:"passId"`
	DocumentRef     runstore.ContentRef `json:"documentRef"`
	Route           config.Route        `json:"route"`
	Prepared        BundlePreparedPlan  `json:"prepared"`
	DeadlineSeconds int                 `json:"deadlineSeconds"`
}

type BundlePreparedPlan struct {
	Version        int                   `json:"version"`
	Purpose        int                   `json:"purpose"`
	GraphSha256    string                `json:"graphSha256"`
	Batches        []runstore.ContentRef `json:"batches"`
	Limitations    []string              `json:"limitations"`
	IsComplete     bool                  `json:"isComplete"`
	GraphPreflight json.RawMessage       `json:"graphPreflight"`
	SplitReason    *string               `json:"splitReason"`
}

type BundlePreparedBatch struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Kind           string               `json:"kind"`
	DocumentSha256 string               `json:"documentSha256"`
	Batch          review.PreparedBatch `json:"batch"`
}

type BundleStore struct {
	Directory string
	Content   *runstore.ContentStore
	Manifest  ArtifactBundle
	document  review.Document
}

type BundleResourcePolicy struct {
	MaximumUniqueBytes int64 `json:"maximumUniqueBytes"`
	MaximumParts       int   `json:"maximumParts"`
	MaximumPartBytes   int   `json:"maximumPartBytes"`
	DeadlineSeconds    int   `json:"deadlineSeconds"`
}

func DefaultBundleResourcePolicy() BundleResourcePolicy {
	return BundleResourcePolicy{runstore.MaximumBundleBytes, runstore.MaximumContentParts, runstore.MaximumContentPartBytes, 3600}
}

func OpenBundle(directory, invocation string) (*BundleStore, error) {
	return OpenBundleWithPolicy(directory, invocation, DefaultBundleResourcePolicy())
}

func OpenBundleWithPolicy(directory, invocation string, policy BundleResourcePolicy) (*BundleStore, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	b := &BundleStore{Directory: directory}
	raw, err := runstore.ReadPrivate(filepath.Join(directory, "bundle.json"), runstore.MaximumDocumentBytes)
	if errors.Is(err, os.ErrNotExist) {
		b.Manifest = ArtifactBundle{SchemaVersion: 2, Kind: "ReviewArtifactBundle", Producer: Identity(), Compatibility: Contract(), InvocationSha256: invocation, RunID: UUID(), PassID: UUID(), SelectionVersion: review.CurrentSelectionVersion, Entries: map[string]runstore.ContentRef{}, Status: "prepared", StartedAt: time.Now().UTC(), DeadlineSeconds: policy.DeadlineSeconds, ResourcePolicy: policy}
	} else {
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &b.Manifest) != nil || b.Manifest.SchemaVersion != 2 || b.Manifest.Kind != "ReviewArtifactBundle" || b.Manifest.Compatibility != Contract() || b.Manifest.Entries == nil || b.Manifest.StartedAt.IsZero() || b.Manifest.DeadlineSeconds < 1 || b.Manifest.DeadlineSeconds > 86400 {
			return nil, errors.New("saved bundle contract is invalid")
		}
		if invocation != "" && (b.Manifest.InvocationSha256 != invocation || b.Manifest.ResourcePolicy != policy) {
			return nil, errors.New("saved bundle belongs to a different invocation or resource policy")
		}
		policy = b.Manifest.ResourcePolicy
	}
	if policy.DeadlineSeconds < 1 || policy.DeadlineSeconds > 86400 || policy.MaximumPartBytes != runstore.MaximumContentPartBytes || b.Manifest.DeadlineSeconds != policy.DeadlineSeconds {
		return nil, errors.New("bundle resource policy is invalid")
	}
	b.Content, err = runstore.NewContentStoreWithLimits(directory, runstore.ContentLimits{MaximumUniqueBytes: policy.MaximumUniqueBytes, MaximumParts: policy.MaximumParts})
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (b *BundleStore) SaveManifest() error {
	return b.SaveManifestContext(context.Background())
}

func (b *BundleStore) SaveManifestContext(ctx context.Context) error {
	return runstore.AtomicJSONContext(ctx, filepath.Join(b.Directory, "bundle.json"), b.Manifest)
}

func (b *BundleStore) SaveSnapshot(snapshot Snapshot) error {
	return b.SaveSnapshotContext(context.Background(), snapshot)
}

func (b *BundleStore) SaveSnapshotContext(ctx context.Context, snapshot Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, err := b.Content.PutJSONContext(ctx, snapshot)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	b.Manifest.Entries["snapshot"] = ref
	b.Manifest.BaseSha, b.Manifest.StartSha, b.Manifest.HeadSha = snapshot.Snapshot.BaseSha, snapshot.Snapshot.StartSha, snapshot.Snapshot.HeadSha
	return b.SaveManifestContext(ctx)
}

func (b *BundleStore) Snapshot() (Snapshot, error) {
	return b.SnapshotContext(context.Background())
}

func (b *BundleStore) SnapshotContext(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	ref, ok := b.Manifest.Entries["snapshot"]
	if !ok {
		return snapshot, errors.New("bundle source snapshot is missing")
	}
	if err := b.Content.DecodeContext(ctx, ref, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Kind != "ReviewSnapshot" || snapshot.SchemaVersion != 1 || snapshot.Compatibility != Contract() {
		return snapshot, errors.New("bundle source snapshot contract is invalid")
	}
	return snapshot, review.ValidateSnapshotContext(ctx, snapshot.Snapshot)
}

func (b *BundleStore) SharedDocument() (Document, error) {
	return b.SharedDocumentContext(context.Background())
}

func (b *BundleStore) SharedDocumentContext(ctx context.Context) (Document, error) {
	var document Document
	if err := ctx.Err(); err != nil {
		return document, err
	}
	ref, ok := b.Manifest.Entries["document"]
	if !ok {
		return document, errors.New("shared document is missing")
	}
	if err := b.Content.DecodeContext(ctx, ref, &document); err != nil {
		return document, err
	}
	if err := ctx.Err(); err != nil {
		return document, err
	}
	if document.Kind != "ReviewDocument" || document.SchemaVersion != 1 || document.Compatibility != Contract() || document.Document.BaseSha != b.Manifest.BaseSha || document.Document.StartSha != b.Manifest.StartSha || document.Document.HeadSha != b.Manifest.HeadSha || document.Document.SelectionVersion != b.Manifest.SelectionVersion {
		return document, errors.New("shared document identity is invalid")
	}
	return document, nil
}

func (b *BundleStore) PackPrepared(prepared review.PreparedBatch) (runstore.ContentRef, error) {
	prepared.Input.Graph = review.Graph{Nodes: []review.GraphNode{}, Edges: []review.GraphEdge{}, Limitations: []string{}, Sha256: prepared.Input.Graph.Sha256}
	return b.Content.PutJSON(BundlePreparedBatch{2, "ReviewPreparedBatch", b.Manifest.Entries["document"].Sha256, prepared})
}

func (b *BundleStore) ReadPrepared(ref runstore.ContentRef) (review.PreparedBatch, error) {
	return b.ReadPreparedContext(context.Background(), ref)
}

func (b *BundleStore) ReadPreparedContext(ctx context.Context, ref runstore.ContentRef) (review.PreparedBatch, error) {
	var stored BundlePreparedBatch
	if err := ctx.Err(); err != nil {
		return review.PreparedBatch{}, err
	}
	if err := b.Content.DecodeContext(ctx, ref, &stored); err != nil {
		return review.PreparedBatch{}, err
	}
	if err := ctx.Err(); err != nil {
		return review.PreparedBatch{}, err
	}
	batch := stored.Batch
	g := batch.Input.Graph
	d := b.document
	if stored.SchemaVersion != 2 || stored.Kind != "ReviewPreparedBatch" || stored.DocumentSha256 != b.Manifest.Entries["document"].Sha256 || len(g.Nodes) != 0 || len(g.Edges) != 0 || len(g.Limitations) != 0 || g.Sha256 != d.Graph.Sha256 || batch.Input.Batch.GraphSha256 != d.Graph.Sha256 || batch.Input.BaseSha != d.BaseSha || batch.Input.StartSha != d.StartSha || batch.Input.HeadSha != d.HeadSha || batch.Input.ManifestSha256 != d.ManifestSha256 {
		return batch, errors.New("prepared reference does not match its shared document")
	}
	batch.Input.Graph = d.Graph
	serialized, err := review.SerializeInputContext(ctx, batch.Input)
	if err != nil {
		return batch, err
	}
	if batch.InputSha256 != review.Hash(batch.UserPrompt) || batch.UserPrompt != serialized {
		return batch, errors.New("prepared reference input hash is invalid")
	}
	if err := ctx.Err(); err != nil {
		return review.PreparedBatch{}, err
	}
	return batch, nil
}

func (b *BundleStore) SavePlan(plan Plan) error {
	return b.SavePlanContext(context.Background(), plan)
}

func (b *BundleStore) SavePlanContext(ctx context.Context, plan Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Manifest.BaseSha != "" && (b.Manifest.BaseSha != plan.Document.BaseSha || b.Manifest.StartSha != plan.Document.StartSha || b.Manifest.HeadSha != plan.Document.HeadSha) {
		return errors.New("plan source differs from the saved snapshot")
	}
	b.Manifest.BaseSha, b.Manifest.StartSha, b.Manifest.HeadSha = plan.Document.BaseSha, plan.Document.StartSha, plan.Document.HeadSha
	b.document = plan.Document
	doc := Document{1, "ReviewDocument", plan.Producer, plan.Compatibility, plan.Document, nil}
	if len(plan.Prepared.Batches) > 0 {
		doc.Task = plan.Prepared.Batches[0].Input.Task
	}
	var document runstore.ContentRef
	var err error
	if existing, ok := b.Manifest.Entries["document"]; ok {
		original, e := b.SharedDocumentContext(ctx)
		if e != nil {
			return e
		}
		a, e := json.Marshal(original.Document)
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		c, e := json.Marshal(plan.Document)
		if e != nil {
			return e
		}
		if !bytes.Equal(a, c) {
			return errors.New("shared document is immutable")
		}
		document = existing
	} else {
		if err = ctx.Err(); err != nil {
			return err
		}
		document, err = b.putDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	b.Manifest.Entries["document"] = document
	b.Manifest.RunID, b.Manifest.PassID = plan.RunID, plan.PassID
	b.Manifest.SelectionVersion = plan.Document.SelectionVersion
	if err = b.SaveManifestContext(ctx); err != nil {
		return err
	}
	p := BundlePlan{2, "ReviewPlan", plan.Producer, plan.Compatibility, plan.RunID, plan.PassID, document, plan.Route,
		BundlePreparedPlan{plan.Prepared.Version, plan.Prepared.Purpose, plan.Prepared.GraphSha256, []runstore.ContentRef{}, plan.Prepared.Limitations, plan.Prepared.IsComplete, plan.Prepared.GraphPreflight, plan.Prepared.SplitReason}, plan.DeadlineSeconds}
	for _, batch := range plan.Prepared.Batches {
		if err = ctx.Err(); err != nil {
			return err
		}
		prepared := batch
		prepared.Input.Graph = review.Graph{Nodes: []review.GraphNode{}, Edges: []review.GraphEdge{}, Limitations: []string{}, Sha256: prepared.Input.Graph.Sha256}
		ref, err := b.Content.PutJSONContext(ctx, BundlePreparedBatch{2, "ReviewPreparedBatch", b.Manifest.Entries["document"].Sha256, prepared})
		if err != nil {
			return err
		}
		p.Prepared.Batches = append(p.Prepared.Batches, ref)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = runstore.AtomicJSONContext(ctx, filepath.Join(b.Directory, "plan.json"), p); err != nil {
		return err
	}
	raw, err := runstore.ReadPrivateContext(ctx, filepath.Join(b.Directory, "plan.json"), runstore.MaximumDocumentBytes)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	ref, err := b.Content.PutContext(ctx, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	semantic, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	b.Manifest.Entries["plan"] = ref
	b.Manifest.PlanHash = review.Hash("review-plan-v2\n" + string(semantic))
	b.Manifest.PlanFileSha256 = ref.Sha256
	b.Manifest.RunID, b.Manifest.PassID = plan.RunID, plan.PassID
	b.Manifest.SelectionVersion = plan.Document.SelectionVersion
	b.Manifest.Status = "planned"
	return b.SaveManifestContext(ctx)
}

func (b *BundleStore) LoadPlan() (Plan, error) {
	return b.LoadPlanContext(context.Background())
}

func (b *BundleStore) LoadPlanContext(ctx context.Context) (Plan, error) {
	var plan Plan
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	ref, ok := b.Manifest.Entries["plan"]
	if !ok {
		return plan, errors.New("bundle plan is missing")
	}
	var stored BundlePlan
	if err := b.Content.DecodeContext(ctx, ref, &stored); err != nil {
		return plan, err
	}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	raw, err := runstore.ReadPrivateContext(ctx, filepath.Join(b.Directory, "plan.json"), runstore.MaximumDocumentBytes)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return plan, err
	}
	if err != nil || review.Hash(string(raw)) != b.Manifest.PlanFileSha256 || ref.Sha256 != b.Manifest.PlanFileSha256 {
		return plan, errors.New("saved physical bundle plan changed")
	}
	semantic, err := json.Marshal(stored)
	if err != nil || review.Hash("review-plan-v2\n"+string(semantic)) != b.Manifest.PlanHash || stored.SchemaVersion != 2 || stored.Kind != "ReviewPlan" || stored.Compatibility != Contract() || stored.RunID != b.Manifest.RunID || stored.PassID != b.Manifest.PassID || stored.DeadlineSeconds != b.Manifest.DeadlineSeconds || stored.DocumentRef.Sha256 != b.Manifest.Entries["document"].Sha256 {
		return plan, errors.New("saved bundle plan identity is invalid")
	}
	var document Document
	if err = b.Content.DecodeContext(ctx, stored.DocumentRef, &document); err != nil {
		return plan, err
	}
	if err = ctx.Err(); err != nil {
		return plan, err
	}
	if document.Kind != "ReviewDocument" || document.SchemaVersion != 1 || document.Compatibility != Contract() || document.Document.BaseSha != b.Manifest.BaseSha || document.Document.StartSha != b.Manifest.StartSha || document.Document.HeadSha != b.Manifest.HeadSha || document.Document.SelectionVersion != b.Manifest.SelectionVersion {
		return plan, errors.New("saved shared document belongs to a different source")
	}
	b.document = document.Document
	if _, ok := b.Manifest.Entries["snapshot"]; ok {
		snapshot, err := b.SnapshotContext(ctx)
		if err != nil {
			return plan, err
		}
		if snapshot.Snapshot.BaseSha != b.Manifest.BaseSha || snapshot.Snapshot.StartSha != b.Manifest.StartSha || snapshot.Snapshot.HeadSha != b.Manifest.HeadSha || snapshot.Snapshot.ManifestSha256 != document.Document.ManifestSha256 {
			return plan, errors.New("shared document does not match its source snapshot")
		}
	}
	prepared := review.PreparedPlan{Version: stored.Prepared.Version, Purpose: stored.Prepared.Purpose, GraphSha256: stored.Prepared.GraphSha256, Limitations: stored.Prepared.Limitations, IsComplete: stored.Prepared.IsComplete, GraphPreflight: stored.Prepared.GraphPreflight, SplitReason: stored.Prepared.SplitReason, Batches: []review.PreparedBatch{}}
	for _, ref := range stored.Prepared.Batches {
		batch, err := b.ReadPreparedContext(ctx, ref)
		if err != nil {
			return plan, err
		}
		prepared.Batches = append(prepared.Batches, batch)
	}
	plan = Plan{SchemaVersion: 1, Kind: "ReviewPlan", Producer: stored.Producer, Compatibility: stored.Compatibility, RunID: stored.RunID, PassID: stored.PassID, Document: b.document, Route: stored.Route, Prepared: prepared, DeadlineSeconds: stored.DeadlineSeconds, bundle: b}
	return plan, plan.ValidateContext(ctx)
}

func (b *BundleStore) RecoverPlanMetadata() (bool, error) {
	return b.RecoverPlanMetadataContext(context.Background())
}

func (b *BundleStore) RecoverPlanMetadataContext(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, ok := b.Manifest.Entries["plan"]; ok {
		return true, nil
	}
	raw, err := runstore.ReadPrivateContext(ctx, filepath.Join(b.Directory, "plan.json"), runstore.MaximumDocumentBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stored BundlePlan
	if json.Unmarshal(raw, &stored) != nil || stored.SchemaVersion != 2 || stored.Kind != "ReviewPlan" || stored.DocumentRef.Sha256 != b.Manifest.Entries["document"].Sha256 || stored.RunID != b.Manifest.RunID || stored.PassID != b.Manifest.PassID {
		return false, errors.New("uncommitted physical plan does not match its invocation")
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	ref, err := b.Content.PutContext(ctx, bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	semantic, err := json.Marshal(stored)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	b.Manifest.Entries["plan"] = ref
	b.Manifest.PlanHash = review.Hash("review-plan-v2\n" + string(semantic))
	b.Manifest.PlanFileSha256 = ref.Sha256
	if _, err = b.LoadPlanContext(ctx); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	return true, b.SaveManifestContext(ctx)
}

func (b *BundleStore) SaveResult(result Result) error {
	result.Producer = b.Manifest.Producer
	ref, err := b.Content.PutJSON(result)
	if err != nil {
		return err
	}
	b.Manifest.Entries["result"] = ref
	b.Manifest.Status = result.Status
	return b.SaveManifest()
}

// Each collection is encoded separately to avoid a full document-sized JSON buffer.
func (b *BundleStore) putDocument(document Document) (runstore.ContentRef, error) {
	return b.putDocumentContext(context.Background(), document)
}

func (b *BundleStore) putDocumentContext(ctx context.Context, document Document) (runstore.ContentRef, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := writeDocument(contextDocumentWriter{ctx, writer}, document)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	ref, err := b.Content.PutContext(ctx, reader)
	_ = reader.CloseWithError(err)
	producerErr := <-done
	if err == nil {
		err = producerErr
	}
	return ref, err
}

type contextDocumentWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextDocumentWriter) Write(raw []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(raw)
}

func writeDocument(writer io.Writer, document Document) error {
	encoder := json.NewEncoder(writer)
	write := func(value string) error { _, err := io.WriteString(writer, value); return err }
	if err := write(`{"schemaVersion":1,"kind":"ReviewDocument","producer":`); err != nil {
		return err
	}
	if err := encoder.Encode(document.Producer); err != nil {
		return err
	}
	if err := write(`,"compatibility":`); err != nil {
		return err
	}
	if err := encoder.Encode(document.Compatibility); err != nil {
		return err
	}
	if err := write(`,"task":null,"document":`); err != nil {
		return err
	}
	d := document.Document
	graph, blocks, dependencies, declarations := d.Graph, d.Blocks, d.DependencyContext, d.DeclarationContext
	d.Graph = review.Graph{}
	d.Blocks = nil
	d.DependencyContext = nil
	d.DeclarationContext = nil
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"graph", "blocks", "dependencyContext", "declarationContext"} {
		delete(fields, name)
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	if _, err = writer.Write(raw[:len(raw)-1]); err != nil {
		return err
	}
	if err = write(`,"graph":{"sha256":`); err != nil {
		return err
	}
	if err = encoder.Encode(graph.Sha256); err != nil {
		return err
	}
	if err = write(`,"limitations":`); err != nil {
		return err
	}
	if err = encoder.Encode(graph.Limitations); err != nil {
		return err
	}
	if err = write(`,"nodes":`); err != nil {
		return err
	}
	if err = writeArray(writer, graph.Nodes); err != nil {
		return err
	}
	if err = write(`,"edges":`); err != nil {
		return err
	}
	if err = writeArray(writer, graph.Edges); err != nil {
		return err
	}
	if err = write(`},"blocks":`); err != nil {
		return err
	}
	if err = writeArray(writer, blocks); err != nil {
		return err
	}
	if err = write(`,"dependencyContext":`); err != nil {
		return err
	}
	if err = writeArray(writer, dependencies); err != nil {
		return err
	}
	if err = write(`,"declarationContext":`); err != nil {
		return err
	}
	if err = writeArray(writer, declarations); err != nil {
		return err
	}
	return write("}}\n")
}

func writeArray[T any](writer io.Writer, values []T) error {
	if values == nil {
		_, err := io.WriteString(writer, "null")
		return err
	}
	if _, err := io.WriteString(writer, "["); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	for i, value := range values {
		if i > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if err := encoder.Encode(value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]")
	return err
}

func (b *BundleStore) PlanBytes() ([]byte, error) {
	return b.PlanBytesContext(context.Background())
}

func (b *BundleStore) PlanBytesContext(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref, ok := b.Manifest.Entries["plan"]
	if !ok {
		return nil, fmt.Errorf("bundle plan is missing")
	}
	return b.Content.BytesContext(ctx, ref)
}

package review

import "encoding/json"

// Revision distinguishes base evidence from head evidence in a pinned comparison.
type Revision int

// Base and Head identify the two source revisions used by changed lines and evidence.
const (
	Base Revision = iota
	Head
)

// ArchitectureReview preserves the protocol purpose identifier used by review consumers.
const ArchitectureReview = 3

// PrepareOptions selects pinned revisions and bounds source size before Git objects are read.
type PrepareOptions struct {
	Repository, Base, Start, Head string
	ExactBase                     bool
	MaxFileBytes                  int64
}

// Snapshot binds file content and diffs to exact Git revisions for reproducible analysis.
type Snapshot struct {
	Version        int            `json:"version"`
	Repository     string         `json:"repository"`
	BaseSha        string         `json:"baseSha"`
	StartSha       string         `json:"startSha"`
	HeadSha        string         `json:"headSha"`
	ManifestSha256 string         `json:"manifestSha256"`
	Files          []FileSnapshot `json:"files"`
	Limitations    []string       `json:"limitations"`
	IsComplete     bool           `json:"isComplete"`
}

// SourceSnapshot retains source content with its revision and digest so evidence can be verified.
type SourceSnapshot struct {
	Path      string   `json:"path"`
	Revision  Revision `json:"revision"`
	CommitSha string   `json:"commitSha"`
	Sha256    string   `json:"sha256"`
	Content   string   `json:"content"`
}

// FileSnapshot keeps both sides of a change with explicit reasons for unavailable source.
type FileSnapshot struct {
	OldPath      string          `json:"oldPath"`
	NewPath      string          `json:"newPath"`
	Operation    string          `json:"operation"`
	Base         *SourceSnapshot `json:"base"`
	Head         *SourceSnapshot `json:"head"`
	IsBinary     bool            `json:"isBinary"`
	IsLFS        bool            `json:"isLfs"`
	IsSubmodule  bool            `json:"isSubmodule"`
	SkipReason   string          `json:"skipReason,omitempty"`
	Diff         string          `json:"diff"`
	Hunks        []Hunk          `json:"hunks"`
	ChangedLines []ChangedLine   `json:"changedLines"`
}

// Hunk retains the diff ranges needed to map changed lines to their source revisions.
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldCount int      `json:"oldCount"`
	NewStart int      `json:"newStart"`
	NewCount int      `json:"newCount"`
	Lines    []string `json:"lines"`
}

// ChangedLine pairs a changed line number with the revision that contains it.
type ChangedLine struct {
	Revision Revision `json:"revision"`
	Line     int      `json:"line"`
}

// CodeSlice marks whether supplied source covers a complete symbol or only a bounded excerpt.
type CodeSlice struct {
	Path            string   `json:"path"`
	Revision        Revision `json:"revision"`
	StartLine       int      `json:"startLine"`
	EndLine         int      `json:"endLine"`
	Content         string   `json:"content"`
	SymbolStartLine int      `json:"symbolStartLine"`
	SymbolEndLine   int      `json:"symbolEndLine"`
	IsComplete      bool     `json:"isComplete"`
}

// Block keeps a review unit attached to its changed lines and original source identity.
type Block struct {
	ID                string        `json:"id"`
	FilePath          string        `json:"filePath"`
	Kind              string        `json:"kind"`
	SymbolIDs         []string      `json:"symbolIds"`
	Base              *CodeSlice    `json:"base"`
	Head              *CodeSlice    `json:"head"`
	ChangedLines      []ChangedLine `json:"changedLines"`
	OriginalBlockID   string        `json:"originalBlockId"`
	BaseExists        bool          `json:"baseExists"`
	HeadExists        bool          `json:"headExists"`
	PartIndex         int           `json:"partIndex"`
	PartCount         int           `json:"partCount"`
	Limitations       []string      `json:"limitations"`
	StructuralContext []CodeSlice   `json:"structuralContext"`
}

// GraphNode binds a symbol to its source revision so dependency claims can be checked.
type GraphNode struct {
	ID                string   `json:"id"`
	SourceSymbolID    string   `json:"sourceSymbolId"`
	Path              string   `json:"path"`
	DeclaringType     *string  `json:"declaringType"`
	Name              string   `json:"name"`
	Signature         string   `json:"signature"`
	Kind              int      `json:"kind"`
	Revision          Revision `json:"revision"`
	Change            int      `json:"change"`
	StartLine         int      `json:"startLine"`
	EndLine           int      `json:"endLine"`
	CounterpartID     *string  `json:"counterpartId"`
	CounterpartAbsent bool     `json:"counterpartAbsent"`
}

// GraphEdge records dependency resolution and uncertainty without inventing source relationships.
type GraphEdge struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	SourceID     string   `json:"sourceId"`
	TargetIDs    []string `json:"targetIds"`
	Revision     Revision `json:"revision"`
	Line         *int     `json:"line"`
	InvokedName  *string  `json:"invokedName"`
	ReceiverType *string  `json:"receiverType"`
	Resolution   int      `json:"resolution"`
	Change       string   `json:"change"`
	Confidence   *float64 `json:"confidence"`
	Reasons      []string `json:"reasons"`
}

// Graph keeps symbol relationships with their limitations and deterministic content identity.
type Graph struct {
	Nodes       []GraphNode `json:"nodes"`
	Edges       []GraphEdge `json:"edges"`
	Limitations []string    `json:"limitations"`
	Sha256      string      `json:"sha256"`
}

// ChangeDisposition explains why a changed line is context or excluded from review coverage.
type ChangeDisposition struct {
	Path     string   `json:"path"`
	Revision Revision `json:"revision"`
	Line     int      `json:"line"`
	Reason   string   `json:"reason"`
}

// Coverage records completeness separately from exclusions so missing input remains visible.
type Coverage struct {
	ExpectedChangedLines int                 `json:"expectedChangedLines"`
	CoveredChangedLines  int                 `json:"coveredChangedLines"`
	RequiredSourceLines  int                 `json:"requiredSourceLines"`
	IsComplete           bool                `json:"isComplete"`
	Exceptions           []string            `json:"exceptions"`
	ContextLines         []ChangeDisposition `json:"contextLines"`
	ExcludedLines        []ChangeDisposition `json:"excludedLines"`
}

// Document keeps review blocks, dependency context, and coverage on the same source baseline.
type Document struct {
	Version            int         `json:"version"`
	ManifestSha256     string      `json:"manifestSha256"`
	BaseSha            string      `json:"baseSha"`
	StartSha           string      `json:"startSha"`
	HeadSha            string      `json:"headSha"`
	Blocks             []Block     `json:"blocks"`
	Graph              Graph       `json:"graph"`
	Coverage           Coverage    `json:"coverage"`
	Limitations        []string    `json:"limitations"`
	DependencyContext  []CodeSlice `json:"dependencyContext"`
	SelectionVersion   int         `json:"selectionVersion"`
	DeclarationContext []CodeSlice `json:"declarationContext"`
}

// FilePart tracks a file across split batches so consumers can explain partial input.
type FilePart struct {
	Path  string `json:"path"`
	Index int    `json:"index"`
	Count int    `json:"count"`
}

// Batch groups source blocks with the graph identity required for one provider request.
type Batch struct {
	Index         int        `json:"index"`
	Count         int        `json:"count"`
	Blocks        []Block    `json:"blocks"`
	GraphSha256   string     `json:"graphSha256"`
	FileParts     []FilePart `json:"fileParts"`
	IsWholeReview bool       `json:"isWholeReview"`
}

// BatchInput binds source revisions and task context to the exact batch sent for review.
type BatchInput struct {
	Version        int             `json:"version"`
	Purpose        int             `json:"purpose"`
	BaseSha        string          `json:"baseSha"`
	StartSha       string          `json:"startSha"`
	HeadSha        string          `json:"headSha"`
	ManifestSha256 string          `json:"manifestSha256"`
	Graph          Graph           `json:"graph"`
	Batch          Batch           `json:"batch"`
	Limitations    []string        `json:"limitations"`
	Task           json.RawMessage `json:"task"`
}

// Budget reserves schema, output, and safety capacity before source input is allocated.
type Budget struct {
	ContextLength            int `json:"contextLength"`
	ReservedOutputTokens     int `json:"reservedOutputTokens"`
	EstimatedSystemTokens    int `json:"estimatedSystemTokens"`
	EstimatedSchemaTokens    int `json:"estimatedSchemaTokens"`
	FixedOverheadTokens      int `json:"fixedOverheadTokens"`
	AvailableUserInputTokens int `json:"availableUserInputTokens"`
}

// TokenCount records how token usage was estimated and whether the count is exact.
type TokenCount struct {
	TemplatedInputTokens     int    `json:"templatedInputTokens"`
	SchemaReserveTokens      int    `json:"schemaReserveTokens"`
	Method                   string `json:"method"`
	IsExact                  bool   `json:"isExact"`
	Limitation               string `json:"limitation"`
	SchemaTokenizationMethod string `json:"schemaTokenizationMethod"`
}

// PreparedBatch binds prompts, limits, and input identity so execution can detect altered batches.
type PreparedBatch struct {
	Input                 BatchInput  `json:"input"`
	Title                 string      `json:"title"`
	SystemPrompt          string      `json:"systemPrompt"`
	UserPrompt            string      `json:"userPrompt"`
	Budget                Budget      `json:"budget"`
	EstimatedInputTokens  int         `json:"estimatedInputTokens"`
	EstimationMethod      string      `json:"estimationMethod"`
	InputSha256           string      `json:"inputSha256"`
	OutputRecoveryEnabled bool        `json:"outputRecoveryEnabled"`
	MaximumOutputTokens   int         `json:"maximumOutputTokens"`
	TokenCount            *TokenCount `json:"tokenCount"`
	ParentTaskID          *string     `json:"parentTaskId"`
	ReplanDepth           int         `json:"replanDepth"`
}

// PreparedPlan retains all batches and completeness metadata before provider execution starts.
type PreparedPlan struct {
	Version        int             `json:"version"`
	Purpose        int             `json:"purpose"`
	GraphSha256    string          `json:"graphSha256"`
	Batches        []PreparedBatch `json:"batches"`
	Limitations    []string        `json:"limitations"`
	IsComplete     bool            `json:"isComplete"`
	GraphPreflight json.RawMessage `json:"graphPreflight"`
	SplitReason    *string         `json:"splitReason"`
}

// PlanOptions supplies prompt and provider limits used to build prepared batches.
type PlanOptions struct {
	SystemPrompt                              string
	ContextLength, OutputTokens, SafetyTokens int
	Provider                                  string
	Task                                      json.RawMessage
}

// Identity binds validation decisions to their run, pass, cluster, and policy version.
type Identity struct {
	RunID, PassID, ClusterID string
	PolicyVersion            int
}

// Evidence ties a quoted source range to one supplied block and revision.
type Evidence struct {
	BlockID   string `json:"blockId"`
	Revision  string `json:"revision"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Quote     string `json:"quote"`
}

// Position keeps old and new coordinates distinct when findings are attached to a diff.
type Position struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
	OldLine *int   `json:"oldLine"`
	NewLine *int   `json:"newLine"`
}

// Finding retains validated evidence and stable identity for an actionable review result.
type Finding struct {
	ID                      string    `json:"id"`
	ReviewRunID             string    `json:"reviewRunId"`
	ReviewPassID            string    `json:"reviewPassId"`
	ReviewChunkID           string    `json:"reviewChunkId"`
	Purpose                 int       `json:"purpose"`
	RuleID                  string    `json:"ruleId"`
	FilePath                string    `json:"filePath"`
	OldLine                 *int      `json:"oldLine"`
	NewLine                 *int      `json:"newLine"`
	Severity                int       `json:"severity"`
	Title                   string    `json:"title"`
	Message                 string    `json:"message"`
	Evidence                string    `json:"evidence"`
	Confidence              float64   `json:"confidence"`
	Fingerprint             string    `json:"fingerprint"`
	InlinePosition          *Position `json:"inlinePosition"`
	InlineSuppressionReason *string   `json:"inlineSuppressionReason"`
}

// Strength retains source evidence for a positive review observation.
type Strength struct {
	ReviewRunID   string  `json:"reviewRunId"`
	ReviewPassID  string  `json:"reviewPassId"`
	ReviewChunkID string  `json:"reviewChunkId"`
	Purpose       int     `json:"purpose"`
	RuleID        string  `json:"ruleId"`
	FilePath      string  `json:"filePath"`
	OldLine       *int    `json:"oldLine"`
	NewLine       *int    `json:"newLine"`
	Title         string  `json:"title"`
	Message       string  `json:"message"`
	Evidence      string  `json:"evidence"`
	Confidence    float64 `json:"confidence"`
	Fingerprint   string  `json:"fingerprint"`
}

// CandidateDecision preserves accepted and rejected model items so validation decisions can be inspected.
type CandidateDecision struct {
	ReviewClusterID string          `json:"reviewClusterId"`
	ResultKind      string          `json:"resultKind"`
	ItemIndex       int             `json:"itemIndex"`
	Output          json.RawMessage `json:"output"`
	FilePath        *string         `json:"filePath"`
	Fingerprint     *string         `json:"fingerprint"`
	State           string          `json:"state"`
	ReasonCode      string          `json:"reasonCode,omitempty"`
	Reason          string          `json:"reason,omitempty"`
}

// Claim records the invariant and source range used for duplicate detection.
type Claim struct {
	Fingerprint string `json:"fingerprint"`
	InvariantID string `json:"invariantId"`
	FilePath    string `json:"filePath"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	IsStrength  bool   `json:"isStrength"`
}

// EvidenceCorrection records coordinate repairs without losing the original declared evidence identity.
type EvidenceCorrection struct {
	ReviewClusterID      string `json:"reviewClusterId"`
	ResultKind           string `json:"resultKind"`
	ItemIndex            int    `json:"itemIndex"`
	BlockID              string `json:"blockId"`
	Revision             string `json:"revision"`
	DeclaredStartLine    int    `json:"declaredStartLine"`
	DeclaredEndLine      int    `json:"declaredEndLine"`
	ActualStartLine      int    `json:"actualStartLine"`
	ActualEndLine        int    `json:"actualEndLine"`
	QuoteSha256          string `json:"quoteSha256"`
	CanonicalQuoteSha256 string `json:"canonicalQuoteSha256"`
	CorrectionKind       string `json:"correctionKind"`
}

// DependencyRejection identifies a missing implementation that prevents a dependency claim from being accepted.
type DependencyRejection struct {
	ReviewClusterID string `json:"reviewClusterId"`
	ResultKind      string `json:"resultKind"`
	ItemIndex       int    `json:"itemIndex"`
	EvidenceBlockID string `json:"evidenceBlockId"`
	EdgeID          string `json:"edgeId"`
	SymbolID        string `json:"symbolId"`
	CallLine        int    `json:"callLine"`
}

// ChangedEvidenceAnchor binds a witness to an exact changed source line and digest.
type ChangedEvidenceAnchor struct {
	FilePath     string `json:"filePath"`
	Revision     string `json:"revision"`
	RevisionSha  string `json:"revisionSha"`
	Line         int    `json:"line"`
	SourceSha256 string `json:"sourceSha256"`
	IsPrimary    bool   `json:"isPrimary"`
}

// FindingWitness retains paired source anchors used to distinguish existing behavior from new defects.
type FindingWitness struct {
	Fingerprint              string                  `json:"fingerprint"`
	RuleID                   string                  `json:"ruleId"`
	InvariantID              string                  `json:"invariantId"`
	HasPairedPrimaryEvidence bool                    `json:"hasPairedPrimaryEvidence"`
	IsNewHelperOnly          bool                    `json:"isNewHelperOnly"`
	Anchors                  []ChangedEvidenceAnchor `json:"anchors"`
	BaseSha                  string                  `json:"baseSha"`
	HeadSha                  string                  `json:"headSha"`
}

// DuplicateRejection explains which validated finding was retained and why another was rejected.
type DuplicateRejection struct {
	RejectedFingerprint string                  `json:"rejectedFingerprint"`
	RetainedFingerprint string                  `json:"retainedFingerprint"`
	Reason              string                  `json:"reason"`
	SharedBaseAnchors   []ChangedEvidenceAnchor `json:"sharedBaseAnchors"`
}

// BatchResult keeps findings, validation decisions, and coverage limitations together.
type BatchResult struct {
	Findings                []Finding             `json:"findings"`
	Strengths               []Strength            `json:"strengths"`
	Limitations             []string              `json:"limitations"`
	IsComplete              bool                  `json:"isComplete"`
	CheckedBlockIDs         []string              `json:"checkedBlockIds"`
	UnconfirmedDependencies []string              `json:"unconfirmedDependencies"`
	RejectedItemCount       int                   `json:"rejectedItemCount"`
	ValidationPolicyVersion int                   `json:"validationPolicyVersion"`
	ModelOutputSha256       *string               `json:"modelOutputSha256"`
	Candidates              []CandidateDecision   `json:"candidates"`
	Claims                  []Claim               `json:"claims"`
	EvidenceCorrections     []EvidenceCorrection  `json:"evidenceCorrections"`
	DependencyRejections    []DependencyRejection `json:"dependencyRejections"`
	FindingWitnesses        []FindingWitness      `json:"findingWitnesses"`
	DuplicateRejections     []DuplicateRejection  `json:"duplicateRejections"`
}

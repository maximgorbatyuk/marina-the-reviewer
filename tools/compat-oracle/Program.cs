using System.Text.Json;
using System.Text.Json.Nodes;
using Petrel.GitLabAi.Application.Ai;
using Petrel.GitLabAi.Application.GitLab;
using Petrel.GitLabAi.Application.Reviews;
using Petrel.GitLabAi.Domain;

var options = SignificantReviewProtocol.JsonOptions;
var run = Guid.Parse("00112233-4455-6677-8899-aabbccddeeff");
var pass = Guid.Parse("ffeeddcc-bbaa-9988-7766-554433221100");
var cluster = Guid.Parse("00000000-0000-0000-0000-000000000001");
var before = new SignificantReviewCodeSlice("A.java", SemanticReviewRevision.Base, 2, 4, " int f() {\n  return 1;\n }", 2, 4, true);
var after = before with { Revision = SemanticReviewRevision.Head, Content = " int f() {\n  return 2;\n }" };
var block = new SignificantReviewBlock("b1", "A.java", "method", [], before, after,
    [new(SemanticReviewRevision.Base, 3), new(SemanticReviewRevision.Head, 3)]);
var graph = new SignificantReviewGraph([], [], [], "graph-hash");
var batch = new SignificantReviewBatch(1, 1, [block], graph.Sha256, [new("A.java", 1, 1)], true);
var input = new SignificantReviewBatchInput(3, PromptPurpose.ArchitectureReview, new('a', 40), new('a', 40), new('b', 40), "manifest", graph, batch, []);
var prepared = new SignificantReviewPreparedBatch(input, "A.java", SignificantReviewProtocol.SystemPrompt(SignificantReviewProtocol.DefaultArchitectureReviewPrompt, PromptPurpose.ArchitectureReview), SignificantReviewProtocol.SerializeInput(input), new(131072, 16384, 0, 1000, 13748, 99940), 1, "oracle", "input-hash");
JsonObject Output(string quote = "  return 2;", int line = 3) => JsonSerializer.SerializeToNode(new {
    checkedBlockIds = new[] { "b1" }, findings = new[] { new {
        blockId = "b1", ruleId = "BEHAVIOR", invariantId = " result ", severity = "Error", title = "Incorrect result", message = "The changed result breaks callers.", confidence = 0.9,
        evidence = new[] { new { blockId = "b1", revision = "head", startLine = line, endLine = line, quote } }, requiredSymbolIds = Array.Empty<string>(), graphEdgeIds = Array.Empty<string>()
    } }, strengths = Array.Empty<object>(), unconfirmedDependencies = Array.Empty<string>()
}, options)!.AsObject();
var cases = new List<object>();
void Case(string name, JsonObject output, SignificantReviewPreparedBatch? custom = null, int policy = 2) {
    var selected = custom ?? prepared;
    object? result = null; string? error = null;
    try { result = SignificantReviewProtocol.Validate(JsonSerializer.SerializeToElement(output, options), selected, run, pass, cluster, policy); }
    catch (JsonException exception) { error = exception.Message; }
    cases.Add(new { name, prepared = selected, output, policy, result, error });
}
Case("head-only", Output());
Case("coordinate-correction", Output(line: 2));
Case("forged-quote", Output("return 99;"));
var baseOnly = Output("  return 1;"); baseOnly["findings"]![0]!["evidence"]![0]!["revision"] = "base"; Case("base-only", baseOnly);
Case("legacy-policy-requires-base", Output(), policy: 1);
var srp = Output(); srp["findings"]![0]!["ruleId"] = "SOLID_SRP"; Case("srp-confidence", srp);
var duplicate = Output(); duplicate["checkedBlockIds"]!.AsArray().Add("b1"); Case("duplicate-checked", duplicate);
var missing = Output(); missing["checkedBlockIds"] = new JsonArray(); Case("missing-checked", missing);
var contradiction = Output(); var strength = contradiction["findings"]![0]!.DeepClone().AsObject(); strength.Remove("severity"); strength["ruleId"] = "STRENGTH_RESULT"; contradiction["strengths"]!.AsArray().Add(strength); Case("contradiction", contradiction);
var indentOutput = Output("int f() {\n return 2;\n}", 2); indentOutput["findings"]![0]!["evidence"]![0]!["endLine"] = 4; Case("java-indent", indentOutput);
var required = Output(); required["findings"]![0]!["requiredSymbolIds"]!.AsArray().Add("n2");
var dependencyNode = new SignificantReviewGraphNode("n2", "s2", "Other.java", "Other", "g", "int g()", SemanticReviewSymbolKind.Method, SemanticReviewRevision.Head, SemanticReviewSymbolChange.Modified, 1, 3, null, false);
var dependencyGraph = graph with { Nodes = [dependencyNode] };
Case("missing-required-body", required, prepared with { Input = input with { Graph = dependencyGraph } });
var callSource = new SignificantReviewGraphNode("n1", "s1", "A.java", "A", "f", "int f()", SemanticReviewSymbolKind.Method, SemanticReviewRevision.Head, SemanticReviewSymbolChange.Modified, 2, 4, null, false);
var callGraph = dependencyGraph with { Nodes = [callSource, dependencyNode], Edges = [new("e1", "call", "n1", ["n2"], SemanticReviewRevision.Head, 3, "g", null, SemanticReviewCallResolution.Resolved, "added")] };
Case("changed-call-missing-body", Output(), prepared with { Input = input with { Graph = callGraph } });
var sourceBefore = "package p;\nclass A {\n int f(int x) {\n  return g(x);\n }\n int g(int x) {\n  return x;\n }\n}\n";
var sourceAfter = sourceBefore.Replace("return g(x)", "return g(x + 1)", StringComparison.Ordinal);
var diff = new DiffFileInput("A.java", "A.java", 10, false, false, false, false, false, [new(4, 4, ["-  return g(x);", "+  return g(x + 1);"])]);
var preparation = new SemanticReviewPreparation(1, 1, new(new('a', 40), new('a', 40), new('b', 40)), "manifest", [new("A.java", "A.java", diff, new("A.java", new('a', 40), SemanticReviewRevision.Base, "before", sourceBefore), new("A.java", new('b', 40), SemanticReviewRevision.Head, "after", sourceAfter))], []);
var symbols = new SemanticReviewAnalyzer().Analyze(preparation);
var document = new SignificantReviewExtractor().Extract(preparation, symbols);
var planned = new SignificantReviewBatchPlanner().Plan(document, PromptPurpose.ArchitectureReview, _ => true);
var projected = SignificantReviewGraphProjector.Create(document.Graph, planned.Batches[0]);
var analyzedInput = input with { Graph = document.Graph, Batch = planned.Batches[0] };
var fixture = new {
    baseline = "acd37b498f1c9367b4044e8f9d5ad55b90617e59", runId = run, passId = pass, clusterId = cluster,
    systemPrompt = prepared.SystemPrompt, responseSchema = SignificantReviewProtocol.Schema.Schema,
    validationCases = cases,
    wireCases = new[] { new { name = "empty-graph", input, wire = prepared.UserPrompt }, new { name = "java-graph", input = analyzedInput, wire = SignificantReviewProtocol.SerializeInput(analyzedInput) } },
    analysisCases = new[] { new { name = "local-java-call", preparation, symbols, document, planned, projection = projected } },
    outputBudgets = new[] { new { context = 131072, blocks = 1, provider = "generic", value = SignificantReviewOutputBudget.Calculate(131072, PromptPurpose.ArchitectureReview, 1) }, new { context = 1048576, blocks = 20, provider = "deepseek", value = SignificantReviewOutputBudget.Calculate(1048576, PromptPurpose.ArchitectureReview, 20, AiProviderIds.Deepseek) }, new { context = 1048576, blocks = 20, provider = "mimo", value = SignificantReviewOutputBudget.Calculate(1048576, PromptPurpose.ArchitectureReview, 20, AiProviderIds.Mimo) } }
};
File.WriteAllText(args[0], JsonSerializer.Serialize(fixture, new JsonSerializerOptions(options) { WriteIndented = true }) + "\n");

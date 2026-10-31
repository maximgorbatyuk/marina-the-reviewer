package review

import (
	"fmt"
	"strings"
	"testing"
)

func TestMRGraphKeepsCallersAndDependenciesWithoutUnrelatedSource(t *testing.T) {
	before := "class Service {\n int load() {\n  return helper();\n }\n int helper() { return 1; }\n int unused() { return 0; }\n}\n"
	after := strings.Replace(before, "return helper();", "return helper() + 1;", 1)
	snapshot := mrGraphSnapshot("Service.java", before, after, 3)
	addMRGraphContext(&snapshot, "Caller.java", "class Caller {\n Service service;\n int run() {\n  return service.load();\n }\n}\n")
	want, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		addMRGraphContext(&snapshot, fmt.Sprintf("AAAUnrelated%d.java", i), fmt.Sprintf("class Unrelated%d {\n int unused() { return 0; }\n}\n", i))
	}
	got, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got.SelectionVersion != 2 {
		t.Fatalf("new analysis did not pin the MR selection policy: %d", got.SelectionVersion)
	}
	if got.Graph.Sha256 != want.Graph.Sha256 {
		t.Fatal("unrelated source changed the selected MR graph")
	}
	names := map[string]bool{}
	for _, node := range got.Graph.Nodes {
		names[node.Name] = true
		if strings.Contains(node.Path, "Unrelated") || node.Name == "unused" {
			t.Fatalf("unrelated symbol remains in the MR graph: %+v", node)
		}
	}
	for _, name := range []string{"load", "helper", "run", "Service", "Caller"} {
		if !names[name] {
			t.Fatalf("required caller or dependency is missing: %s", name)
		}
	}
	if len(got.Blocks) != 1 || !got.Coverage.IsComplete {
		t.Fatalf("MR selection changed primary source coverage: %+v", got.Coverage)
	}
	for _, source := range got.DependencyContext {
		if strings.Contains(source.Path, "Unrelated") || strings.Contains(source.Content, "unused()") {
			t.Fatal("unrelated method body remains in dependency context")
		}
	}
	if err := ValidateDocument(got); err != nil {
		t.Fatal(err)
	}
}

func TestMRGraphKeepsUnresolvedCallsAndRenamedMove(t *testing.T) {
	before := "class Service {\n int first() {\n  return remote.check();\n }\n}\n"
	after := strings.Replace(before, "first()", "second()", 1)
	document, err := Analyze(mrGraphSnapshot("Service.java", before, after, 2))
	if err != nil {
		t.Fatal(err)
	}
	call, move := false, false
	for _, edge := range document.Graph.Edges {
		call = call || edge.Kind == "call" && edge.Resolution == 2 && edge.InvokedName != nil && *edge.InvokedName == "check"
		move = move || edge.Kind == "possible-move" && edge.Change == "unproven" && edge.Resolution == 1 && edge.Confidence != nil && *edge.Confidence == 0.7
	}
	if !call || !move {
		t.Fatalf("unresolved call or unproven renamed move is missing: call=%t move=%t", call, move)
	}
}

func TestMRGraphResolvesInheritedInterfaceAndFrameworkContracts(t *testing.T) {
	for _, tc := range []struct {
		name, source, context string
		targets, resolution   int
	}{
		{"inherited", "class Child extends Parent {\n int f() { return g(); }\n}\n", "class Parent {\n int g() { return 1; }\n}\n", 0, 2},
		{"interface", "class Caller {\n Service service;\n int f() { return service.g(); }\n}\n", "interface Service {\n int g();\n}\nclass Implementation implements Service {\n public int g() { return 1; }\n}\n", 2, 0},
		{"interface comment", "class Caller {\n Service service;\n int f() { return service.g(); }\n}\n", "interface Service {\n /** Contract uses {@code value}. */\n int g();\n}\nclass Implementation implements Service {\n public int g() { return 1; }\n}\n", 2, 0},
		{"framework", "class Caller {\n Repository repository;\n Object f() { return repository.findAll(); }\n}\n", "interface Repository extends JpaRepository<Entity, Long> {\n}\n", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "Changed.java", Operation: "A", Head: &SourceSnapshot{Path: "Changed.java", Revision: Head, Content: tc.source}, ChangedLines: []ChangedLine{{Head, 2}}}}}
			addMRGraphContext(&snapshot, "Dependency.java", tc.context)
			document, err := Analyze(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, edge := range document.Graph.Edges {
				if edge.Kind != "call" {
					continue
				}
				if len(edge.TargetIDs) != tc.targets || edge.Resolution != tc.resolution {
					t.Fatalf("contract resolution differs: %+v", edge)
				}
				return
			}
			t.Fatal("contract call is missing")
		})
	}
}

func TestJavaCallLinesRemainIndependent(t *testing.T) {
	source := "class A {\n int f() {\n  g();\n  g();\n  return 1;\n }\n void g() {}\n}\n"
	for _, version := range []int{1, 2} {
		document, err := analyzeWithSelection(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "A.java", Operation: "A", Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 3}}}}}, version)
		if err != nil {
			t.Fatal(err)
		}
		got := []int{}
		for _, edge := range document.Graph.Edges {
			if edge.Kind == "call" && edge.InvokedName != nil && *edge.InvokedName == "g" {
				got = append(got, *edge.Line)
			}
		}
		if fmt.Sprint(got) != "[3 4]" {
			t.Fatalf("selection %d changed call coordinates: %v", version, got)
		}
	}
}

func TestMRReceiverDoesNotUseAnotherMethodsParameter(t *testing.T) {
	source := "class A {\n B service;\n int f() {\n  return service.g();\n }\n void h(C service) {}\n}\nclass B {\n int g() { return 1; }\n}\nclass C {\n int g() { return 2; }\n}\n"
	document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "A.java", Operation: "A", Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 4}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range document.Graph.Edges {
		if edge.Kind != "call" || edge.InvokedName == nil || *edge.InvokedName != "g" {
			continue
		}
		if edge.Resolution != 0 || len(edge.TargetIDs) != 1 || edge.ReceiverType == nil || *edge.ReceiverType != "B" {
			t.Fatalf("the field receiver was replaced by another method parameter: %+v", edge)
		}
		for _, node := range document.Graph.Nodes {
			if node.ID == edge.TargetIDs[0] && node.DeclaringType != nil && *node.DeclaringType == "B" {
				return
			}
		}
		t.Fatal("the receiver resolved to another class")
	}
	t.Fatal("the scoped receiver call is missing")
}

func TestLegacyAnalysisKeepsItsSelectionPolicy(t *testing.T) {
	before := "class Service {\n int f() { return 1; }\n}\n"
	snapshot := mrGraphSnapshot("Service.java", before, strings.Replace(before, "return 1", "return 2", 1), 2)
	addMRGraphContext(&snapshot, "Unrelated.java", "class Unrelated {\n int g() { return 3; }\n}\n")
	legacy, err := analyzeWithSelection(snapshot, 1)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SelectionVersion != 1 || current.SelectionVersion != CurrentSelectionVersion || len(legacy.Graph.Nodes) <= len(current.Graph.Nodes) {
		t.Fatal("the new selection policy replaced a legacy source document")
	}
}

func TestMRDeclarationCommentsRemainOwnedByTheirSourceUnit(t *testing.T) {
	for _, tc := range []struct {
		name, before, after, kind string
		changed                   []ChangedLine
		first, last               int
	}{
		{"method", "interface Service {\n /** Old contract. */\n int load();\n}\n", "interface Service {\n /** New contract. */\n int load();\n}\n", "method", []ChangedLine{{Base, 2}, {Head, 2}}, 2, 3},
		{"field", "class Service {\n}\n", "class Service {\n // Required value.\n int value;\n}\n", "field", []ChangedLine{{Head, 2}, {Head, 3}}, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := mrGraphSnapshot("Service.java", tc.before, tc.after, 2)
			snapshot.Files[0].ChangedLines = tc.changed
			document, err := Analyze(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Blocks) != 1 || document.Blocks[0].Kind != tc.kind || document.Blocks[0].Head.StartLine != tc.first || document.Blocks[0].Head.EndLine != tc.last {
				t.Fatalf("declaration comments lost source ownership: %+v", document.Blocks)
			}
			if err := ValidateDocument(document); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMRNestedCallsRetainEverySourceInvocation(t *testing.T) {
	before := "class Service {\n int f() {\n  return g(helper());\n }\n int g(int value) { return value; }\n int helper() { return 1; }\n}\n"
	document, err := Analyze(mrGraphSnapshot("Service.java", before, strings.Replace(before, "g(helper())", "g(helper()) + 1", 1), 3))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, edge := range document.Graph.Edges {
		if edge.Kind == "call" && edge.InvokedName != nil {
			names[*edge.InvokedName]++
		}
	}
	if names["g"] != 2 || names["helper"] != 2 {
		t.Fatalf("a nested source invocation was omitted: %v", names)
	}
}

func TestMRUnchangedTryResourcesDoNotCreateChangedDeclarations(t *testing.T) {
	before := "class Service {\n int f() { return 1; }\n}\n"
	snapshot := mrGraphSnapshot("Service.java", before, strings.Replace(before, "return 1", "return 2", 1), 2)
	addMRGraphContext(&snapshot, "Unrelated.java", "class Unrelated {\n void f() {\n  try (InputStream input = open()) {\n   first();\n  }\n  try (InputStream input = open()) {\n   second();\n  }\n }\n}\n")
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Path == "Unrelated.java" {
			t.Fatalf("an unchanged try block entered the MR graph: %+v", node)
		}
	}
}

func TestMRNestedRecordIsNotAContainingTypesMethod(t *testing.T) {
	source := "record Outer() {\n record Inner(String value) {\n }\n}\n"
	document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "Outer.java", Operation: "A", Head: &SourceSnapshot{Path: "Outer.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 2}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Name == "Inner" && node.Kind != 2 {
			t.Fatalf("a nested record became a method of its containing type: %+v", node)
		}
	}
	if len(document.DeclarationContext) != 2 {
		t.Fatalf("the nested record header disappeared from declaration context: %d", len(document.DeclarationContext))
	}
}

func TestMRTypeAnnotationArrayDoesNotHideFirstMethod(t *testing.T) {
	source := "@Client(configuration = {First.class, Second.class})\ninterface Service {\n int first();\n int second();\n}\n"
	document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "Service.java", Operation: "A", Head: &SourceSnapshot{Path: "Service.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 3}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Name == "first" && node.Kind == 4 {
			if node.StartLine != 3 || node.EndLine != 3 || node.Signature != "int first()" {
				t.Fatalf("a type annotation changed its first method bounds: %+v", node)
			}
			if len(document.DeclarationContext) != 1 || document.DeclarationContext[0].EndLine != 2 {
				t.Fatal("the type annotation hid the declaration header")
			}
			return
		}
	}
	t.Fatal("the first annotated type member is missing")
}

func TestMRNestedInterfaceKeepsItsFirstDeclarationInsideTheOwner(t *testing.T) {
	source := "class Outer {\n interface Inner {\n  int first();\n }\n}\n"
	document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "Outer.java", Operation: "A", Head: &SourceSnapshot{Path: "Outer.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 3}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Name == "first" && (node.StartLine != 3 || node.EndLine != 3 || node.Signature != "int first()") {
			t.Fatalf("the outer class expanded its nested interface member: %+v", node)
		}
	}
	if len(document.DeclarationContext) != 2 {
		t.Fatal("the nested interface declaration header disappeared")
	}
}

func TestMRInertUnicodeEscapesDoNotHideDeclarations(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{`\u00a0`, true},
		{`\u0061`, true},
		{`\u0022`, false},
		{`\u000a`, false},
		{`\u005c`, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			source := "class A {\n String f() {\n  return \"" + tc.value + "\";\n }\n}\n"
			document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "A.java", Operation: "A", Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 3}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if (len(document.Graph.Nodes) > 0) != tc.want {
				t.Fatalf("Unicode source safety differs: %s nodes=%d", tc.value, len(document.Graph.Nodes))
			}
			if !tc.want && !strings.Contains(strings.Join(document.Graph.Limitations, "\n"), "Unicode escapes") {
				t.Fatal("unsafe Unicode fallback lost its limitation")
			}
		})
	}
}

func TestMRLanguageNeutralFunctionsKeepRealDeclarations(t *testing.T) {
	before := "export function Component(value) {\n return value;\n}\n"
	snapshot := mrGraphSnapshot("Component.tsx", before, strings.Replace(before, "return value;", "return value + 1;", 1), 2)
	tests := "function render() {\n return Component(1);\n}\nbeforeEach(() => {\n setup();\n});\ntest(\"case\", () => {\n expect(render()).toBe(2);\n});\n"
	snapshot.Files = append(snapshot.Files, FileSnapshot{NewPath: "Component.test.js", Operation: "A", Head: &SourceSnapshot{Path: "Component.test.js", Revision: Head, Content: tests}, ChangedLines: []ChangedLine{{Head, 1}, {Head, 2}, {Head, 4}, {Head, 7}}})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, node := range document.Graph.Nodes {
		names[node.Name]++
		if node.Name != "Component" && node.Name != "render" {
			t.Fatalf("a callback invocation became a declaration: %+v", node)
		}
	}
	if names["Component"] != 2 || names["render"] != 1 {
		t.Fatalf("language-neutral declarations disappeared: %v", names)
	}
	for _, block := range document.Blocks {
		if block.Kind != "file" {
			t.Fatalf("language-neutral graph changed complete-file source scope: %s", block.Kind)
		}
	}
	if !strings.Contains(strings.Join(document.Graph.Limitations, "\n"), "language-neutral") {
		t.Fatal("language-neutral resolution lost its explicit limitation")
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
}

func TestMREnumConstantsRemainOnePairedDeclaration(t *testing.T) {
	before := "enum Failure {\n FIRST(\"first\"),\n LAST(\"last\");\n private final String message;\n Failure(String message) { this.message = message; }\n}\n"
	after := strings.Replace(before, "LAST(\"last\");", "LAST(\"last\"),\n NEXT(\"next\");", 1)
	snapshot := mrGraphSnapshot("Failure.java", before, after, 3)
	snapshot.Files[0].ChangedLines = []ChangedLine{{Base, 3}, {Head, 3}, {Head, 4}}
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Name == "FIRST" || node.Name == "LAST" || node.Name == "NEXT" {
			t.Fatalf("an enum constant became a method declaration: %+v", node)
		}
	}
	if len(document.Blocks) != 1 || document.Blocks[0].Kind != "declaration" || document.Blocks[0].Base == nil || document.Blocks[0].Head == nil {
		t.Fatal("the enum constant list was split into unrelated before and head declarations")
	}
}

func TestMRLanguageNeutralClassKeepsMemberDeclarations(t *testing.T) {
	before := "export class Service {\n execute(value: number): number {\n  return value;\n }\n}\n"
	document, err := Analyze(mrGraphSnapshot("Service.ts", before, strings.Replace(before, "return value;", "return value + 1;", 1), 3))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, node := range document.Graph.Nodes {
		names[node.Name]++
		if node.Name == "execute" && (node.Kind != 4 || node.StartLine != 2 || node.EndLine != 4 || node.DeclaringType == nil || *node.DeclaringType != "Service") {
			t.Fatalf("a language-neutral member lost its declaration bounds: %+v", node)
		}
	}
	if names["Service"] != 2 || names["execute"] != 2 {
		t.Fatalf("language-neutral class members are missing: %v", names)
	}
}

func mrGraphSnapshot(path, before, after string, line int) Snapshot {
	return Snapshot{Version: 1, IsComplete: true, Files: []FileSnapshot{{OldPath: path, NewPath: path, Operation: "M", Base: &SourceSnapshot{Path: path, Revision: Base, Content: before}, Head: &SourceSnapshot{Path: path, Revision: Head, Content: after}, ChangedLines: []ChangedLine{{Base, line}, {Head, line}}}}}
}

func addMRGraphContext(snapshot *Snapshot, path, content string) {
	snapshot.Files = append(snapshot.Files, FileSnapshot{OldPath: path, NewPath: path, Operation: "context", Base: &SourceSnapshot{Path: path, Revision: Base, Content: content}, Head: &SourceSnapshot{Path: path, Revision: Head, Content: content}})
}

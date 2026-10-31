package engine

import (
	"encoding/json"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func TestLegacyAliasedModelPlanRetainsItsSavedOutputBudget(t *testing.T) {
	cfg := config.Config{Version: 1, Routing: config.Routing{Primary: "xiamo-mimo"}, Profiles: map[string]config.Profile{
		"xiamo-mimo": {BaseURL: "https://provider.example/v1", Model: "mimo-v2.6-pro", ContextLength: 1048576},
	}}
	route, err := config.ResolveRouting(cfg, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if current.Prepared.Batches[0].MaximumOutputTokens != 131072 {
		t.Fatal("new alias plan did not freeze the MiMo output budget")
	}
	legacy := current
	legacy.Route.Primary.Profile.MaximumOutputTokens = 0
	legacy.Prepared, err = review.Plan(document(), review.PlanOptions{
		ContextLength: 1048576, Provider: "xiamo-mimo",
	})
	if err != nil || legacy.Prepared.Batches[0].MaximumOutputTokens >= 131072 {
		t.Fatalf("invalid legacy alias fixture: %v", err)
	}
	before, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Plan
	if err = json.Unmarshal(before, &loaded); err != nil {
		t.Fatal(err)
	}
	if err = loaded.Validate(); err != nil {
		t.Fatalf("old frozen alias plan was reinterpreted: %v", err)
	}
	after, err := json.Marshal(loaded)
	if err != nil || string(before) != string(after) {
		t.Fatal("validation changed the legacy plan bytes or hash input")
	}
}

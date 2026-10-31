package engine

import (
	"encoding/json"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func TestSavedPlanKeepsExplicitContextAfterDefaultIncrease(t *testing.T) {
	cfg, _ := routeFor(t, "https://provider.example/v1", "")
	profile := cfg.Profiles["primary"]
	profile.ContextLength = 131072
	cfg.Profiles["primary"] = profile
	route, err := config.ResolveRouting(cfg, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Plan
	if err = json.Unmarshal(before, &loaded); err != nil || loaded.Validate() != nil {
		t.Fatalf("saved explicit context is not valid: %v", err)
	}
	after, err := json.Marshal(loaded)
	if err != nil || string(before) != string(after) || loaded.Route.Primary.Profile.ContextLength != 131072 || loaded.Prepared.Batches[0].Budget.ContextLength != 131072 {
		t.Fatal("validation rewrote the saved context, prepared request, or plan hash input")
	}
}

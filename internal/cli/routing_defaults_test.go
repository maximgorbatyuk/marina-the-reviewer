package cli

import "testing"

func TestDirectProviderRouteUsesOneMillionTokenContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MARINADOREVIEW_CONFIG", "")
	_, route, err := routing(arguments{baseURL: "https://provider.example/v1", model: "direct-model", noFallback: true})
	if err != nil || route.Primary.Profile.ContextLength != 1048576 {
		t.Fatalf("direct provider has the wrong default context: %v %+v", err, route)
	}
}

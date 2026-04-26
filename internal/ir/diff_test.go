package ir

import "testing"

func TestDiffEntityChanges(t *testing.T) {
	from := EmptyModel("p_1", "edge", "old", []Engine{EngineHAProxy})
	from.Frontends = []Frontend{{ID: "fe_web", Name: "web", Bind: ":80"}}
	from.Backends = []Backend{{ID: "be_old", Name: "old", Servers: []string{}}}
	from.Servers = []Server{{ID: "s1", Address: "10.0.0.1", Port: 8080}}

	to := EmptyModel("p_1", "edge-new", "new", []Engine{EngineHAProxy, EngineNginx})
	to.Frontends = []Frontend{{ID: "fe_web", Name: "web", Bind: ":443"}}
	to.Backends = []Backend{{ID: "be_new", Name: "new", Servers: []string{}}}
	to.Servers = []Server{{ID: "s1", Address: "10.0.0.1", Port: 9090}}

	changes := Diff(from, to)
	seen := map[string]bool{}
	for _, change := range changes {
		seen[string(change.Kind)+"|"+change.EntityType+"|"+change.EntityID] = true
	}
	for _, want := range []string{
		"modified|model|p_1",
		"modified|frontend|fe_web",
		"removed|backend|be_old",
		"added|backend|be_new",
		"modified|server|s1",
	} {
		if !seen[want] {
			t.Fatalf("missing change %s in %+v", want, changes)
		}
	}
}

func TestDiffNoChanges(t *testing.T) {
	model := EmptyModel("p_1", "edge", "", []Engine{EngineHAProxy})
	if changes := Diff(model, model); len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

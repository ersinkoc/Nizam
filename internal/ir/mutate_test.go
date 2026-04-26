package ir

import (
	"encoding/json"
	"testing"
)

func TestApplyEntityMutations(t *testing.T) {
	model := EmptyModel("p_1", "edge", "", []Engine{EngineHAProxy})
	frontend := Frontend{ID: "fe_web", Name: "web", Bind: ":80", Protocol: "http"}
	backend := Backend{ID: "be_app", Name: "app", Algorithm: "roundrobin", Servers: []string{}}
	server := Server{ID: "s1", Address: "127.0.0.1", Port: 8080, Weight: 100}
	rule := Rule{ID: "r1", Predicate: Predicate{Type: "path_prefix", Value: "/api/"}, Action: RuleAction{Type: "use_backend", BackendID: "be_app"}}

	var err error
	model, err = Apply(model, mutation("frontend.create", "", "", "", frontend))
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, mutation("backend.create", "", "", "", backend))
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, mutation("server.create", "", "be_app", "", server))
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, mutation("rule.create", "", "", "fe_web", rule))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Frontends) != 1 || len(model.Backends) != 1 || len(model.Servers) != 1 || len(model.Rules) != 1 {
		t.Fatalf("unexpected model counts: %+v", model)
	}
	if model.Backends[0].Servers[0] != "s1" || model.Frontends[0].Rules[0] != "r1" {
		t.Fatalf("references were not attached: %+v", model)
	}

	model, err = Apply(model, mutation("frontend.update", "fe_web", "", "", map[string]any{"bind": ":8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if model.Frontends[0].Bind != ":8080" {
		t.Fatalf("frontend not updated: %+v", model.Frontends[0])
	}

	model, err = Apply(model, Mutation{Type: "view.move", EntityID: "be_app", X: 50, Y: 75})
	if err != nil {
		t.Fatal(err)
	}
	if model.Backends[0].View.X != 50 || model.Backends[0].View.Y != 75 {
		t.Fatalf("backend view not moved: %+v", model.Backends[0].View)
	}
	model, err = Apply(model, Mutation{Type: "view.move", EntityID: "fe_web", X: 15, Y: 25})
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, Mutation{Type: "view.move", EntityID: "r1", X: 35, Y: 45})
	if err != nil {
		t.Fatal(err)
	}
	if model.Frontends[0].View.X != 15 || model.Rules[0].View.X != 35 {
		t.Fatalf("frontend/rule view not moved: %+v %+v", model.Frontends[0].View, model.Rules[0].View)
	}

	model, err = Apply(model, Mutation{Type: "view.zoom", Zoom: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if model.View.Zoom != 1.5 {
		t.Fatalf("zoom not updated: %+v", model.View)
	}
}

func TestApplyDeletesAndErrors(t *testing.T) {
	model := EmptyModel("p_1", "edge", "", []Engine{EngineHAProxy})
	model.Frontends = []Frontend{{ID: "fe_web", Rules: []string{"r1"}}}
	model.Backends = []Backend{{ID: "be_app", Servers: []string{"s1"}}}
	model.Servers = []Server{{ID: "s1", Address: "127.0.0.1", Port: 8080}}
	model.Rules = []Rule{{ID: "r1"}}

	next, err := Apply(model, Mutation{Type: "server.delete", ID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Servers) != 0 || len(next.Backends[0].Servers) != 0 {
		t.Fatalf("server delete did not detach references: %+v", next)
	}
	next, err = Apply(next, Mutation{Type: "rule.delete", ID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Rules) != 0 || len(next.Frontends[0].Rules) != 0 {
		t.Fatalf("rule delete did not detach references: %+v", next)
	}
	if _, err := Apply(model, Mutation{Type: "backend.update", ID: "missing", Data: json.RawMessage(`{"name":"x"}`)}); err == nil {
		t.Fatal("expected missing entity error")
	}
	if _, err := Apply(model, Mutation{Type: "unknown"}); err == nil {
		t.Fatal("expected unsupported mutation error")
	}
	if _, err := Apply(model, Mutation{Type: "frontend.create"}); err == nil {
		t.Fatal("expected missing data error")
	}
	if _, err := Apply(model, Mutation{Type: "frontend.update", ID: "fe_web", Data: json.RawMessage(`{bad`)}); err == nil {
		t.Fatal("expected invalid patch json error")
	}
}

func TestApplyAllMutationBranches(t *testing.T) {
	model := EmptyModel("p_1", "edge", "", []Engine{EngineHAProxy})
	model.Frontends = []Frontend{{ID: "fe_web", Bind: ":80"}}
	model.Backends = []Backend{{ID: "be_app", Servers: []string{"s1"}}}
	model.Servers = []Server{{ID: "s1", Address: "127.0.0.1", Port: 8080}}
	model.Rules = []Rule{{ID: "r1"}}

	var err error
	model, err = Apply(model, mutation("backend.update", "be_app", "", "", map[string]any{"name": "app"}))
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, mutation("server.update", "s1", "", "", map[string]any{"weight": 50}))
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, mutation("rule.update", "r1", "", "", map[string]any{"name": "api"}))
	if err != nil {
		t.Fatal(err)
	}
	if model.Backends[0].Name != "app" || model.Servers[0].Weight != 50 || model.Rules[0].Name != "api" {
		t.Fatalf("updates not applied: %+v", model)
	}
	model, err = Apply(model, Mutation{Type: "frontend.delete", ID: "fe_web"})
	if err != nil {
		t.Fatal(err)
	}
	model, err = Apply(model, Mutation{Type: "backend.delete", ID: "be_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Frontends) != 0 || len(model.Backends) != 0 {
		t.Fatalf("delete branches not applied: %+v", model)
	}
}

func TestApplyHelperErrorBranches(t *testing.T) {
	model := EmptyModel("p_1", "edge", "", []Engine{EngineHAProxy})
	model.Frontends = []Frontend{{ID: "fe_web", Bind: ":80"}}
	if _, err := Apply(model, Mutation{Type: "frontend.update", ID: "fe_web", Data: json.RawMessage(`{"view":"bad"}`)}); err == nil {
		t.Fatal("expected merged entity decode error")
	}
	if _, err := Apply(model, Mutation{Type: "backend.create"}); err == nil {
		t.Fatal("expected backend create missing data error")
	}
	if _, err := Apply(model, Mutation{Type: "server.create"}); err == nil {
		t.Fatal("expected server create missing data error")
	}
	if _, err := Apply(model, Mutation{Type: "rule.create"}); err == nil {
		t.Fatal("expected rule create missing data error")
	}
	model.Metadata = map[string]any{"bad": func() {}}
	if _, err := Apply(model, Mutation{Type: "view.zoom", Zoom: 2}); err == nil {
		t.Fatal("expected apply clone error")
	}
	if _, err := Clone(model); err == nil {
		t.Fatal("expected clone marshal error")
	}
	if got := appendUnique([]string{"s1"}, "s1"); len(got) != 1 {
		t.Fatalf("appendUnique should skip duplicate: %v", got)
	}
	if got := entityID(map[string]any{"id": func() {}}); got != "" {
		t.Fatalf("expected empty entity id for marshal error, got %q", got)
	}
	if got := entityID([]byte("not an object")); got != "" {
		t.Fatalf("expected empty entity id for unmarshal error, got %q", got)
	}
	filtered := filter([]int{1, 2, 3}, func(item int) bool { return item%2 == 1 })
	if len(filtered) != 2 || filtered[0] != 1 || filtered[1] != 3 {
		t.Fatalf("unexpected filter result: %v", filtered)
	}
}

func mutation(kind, id, backendID, entityID string, data any) Mutation {
	raw, _ := json.Marshal(data)
	return Mutation{Type: kind, ID: id, BackendID: backendID, EntityID: entityID, Data: raw}
}

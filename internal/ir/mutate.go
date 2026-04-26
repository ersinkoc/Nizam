package ir

import (
	"encoding/json"
	"errors"
)

type Mutation struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	BackendID string          `json:"backendId,omitempty"`
	EntityID  string          `json:"entityId,omitempty"`
	X         float64         `json:"x,omitempty"`
	Y         float64         `json:"y,omitempty"`
	Zoom      float64         `json:"zoom,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func Apply(m *Model, mutation Mutation) (*Model, error) {
	cp, err := Clone(m)
	if err != nil {
		return nil, err
	}

	switch mutation.Type {
	case "frontend.create":
		var v Frontend
		if err := decode(mutation.Data, &v); err != nil {
			return nil, err
		}
		cp.Frontends = append(cp.Frontends, v)
	case "frontend.update":
		err = updateByID(cp.Frontends, mutation.ID, mutation.Data, func(i int, v Frontend) { cp.Frontends[i] = v })
	case "frontend.delete":
		cp.Frontends = filter(cp.Frontends, func(v Frontend) bool { return v.ID != mutation.ID })
	case "backend.create":
		var v Backend
		if err := decode(mutation.Data, &v); err != nil {
			return nil, err
		}
		cp.Backends = append(cp.Backends, v)
	case "backend.update":
		err = updateByID(cp.Backends, mutation.ID, mutation.Data, func(i int, v Backend) { cp.Backends[i] = v })
	case "backend.delete":
		cp.Backends = filter(cp.Backends, func(v Backend) bool { return v.ID != mutation.ID })
	case "server.create":
		var v Server
		if err := decode(mutation.Data, &v); err != nil {
			return nil, err
		}
		cp.Servers = append(cp.Servers, v)
		for i := range cp.Backends {
			if cp.Backends[i].ID == mutation.BackendID {
				cp.Backends[i].Servers = appendUnique(cp.Backends[i].Servers, v.ID)
			}
		}
	case "server.update":
		err = updateByID(cp.Servers, mutation.ID, mutation.Data, func(i int, v Server) { cp.Servers[i] = v })
	case "server.delete":
		cp.Servers = filter(cp.Servers, func(v Server) bool { return v.ID != mutation.ID })
		for i := range cp.Backends {
			cp.Backends[i].Servers = removeString(cp.Backends[i].Servers, mutation.ID)
		}
	case "rule.create":
		var v Rule
		if err := decode(mutation.Data, &v); err != nil {
			return nil, err
		}
		cp.Rules = append(cp.Rules, v)
		for i := range cp.Frontends {
			if cp.Frontends[i].ID == mutation.EntityID {
				cp.Frontends[i].Rules = appendUnique(cp.Frontends[i].Rules, v.ID)
			}
		}
	case "rule.update":
		err = updateByID(cp.Rules, mutation.ID, mutation.Data, func(i int, v Rule) { cp.Rules[i] = v })
	case "rule.delete":
		cp.Rules = filter(cp.Rules, func(v Rule) bool { return v.ID != mutation.ID })
		for i := range cp.Frontends {
			cp.Frontends[i].Rules = removeString(cp.Frontends[i].Rules, mutation.ID)
		}
	case "view.move":
		moveEntity(cp, mutation.EntityID, mutation.X, mutation.Y)
	case "view.zoom":
		cp.View.Zoom = mutation.Zoom
	default:
		return nil, errors.New("unsupported mutation type")
	}
	if err != nil {
		return nil, err
	}
	return cp, nil
}

func Clone(m *Model) (*Model, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var cp Model
	_ = json.Unmarshal(b, &cp)
	return &cp, nil
}

func decode[T any](raw json.RawMessage, out *T) error {
	if len(raw) == 0 {
		return errors.New("mutation data is required")
	}
	return json.Unmarshal(raw, out)
}

type identified interface{ comparable }

func updateByID[T any](items []T, id string, patch json.RawMessage, set func(int, T)) error {
	for i, item := range items {
		b, _ := json.Marshal(item)
		var base map[string]any
		_ = json.Unmarshal(b, &base)
		if base["id"] != id {
			continue
		}
		var delta map[string]any
		if err := json.Unmarshal(patch, &delta); err != nil {
			return err
		}
		for k, v := range delta {
			base[k] = v
		}
		merged, _ := json.Marshal(base)
		var next T
		if err := json.Unmarshal(merged, &next); err != nil {
			return err
		}
		set(i, next)
		return nil
	}
	return errors.New("entity not found")
}

func filter[T any](items []T, keep func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

func appendUnique(items []string, id string) []string {
	for _, item := range items {
		if item == id {
			return items
		}
	}
	return append(items, id)
}

func removeString(items []string, id string) []string {
	return filter(items, func(item string) bool { return item != id })
}

func moveEntity(m *Model, id string, x, y float64) {
	for i := range m.Frontends {
		if m.Frontends[i].ID == id {
			m.Frontends[i].View = EntityView{X: x, Y: y}
			return
		}
	}
	for i := range m.Backends {
		if m.Backends[i].ID == id {
			m.Backends[i].View = EntityView{X: x, Y: y}
			return
		}
	}
	for i := range m.Rules {
		if m.Rules[i].ID == id {
			m.Rules[i].View = EntityView{X: x, Y: y}
			return
		}
	}
}

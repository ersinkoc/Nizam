package ir

import (
	"encoding/json"
	"reflect"
	"sort"
)

type DiffKind string

const (
	DiffAdded    DiffKind = "added"
	DiffRemoved  DiffKind = "removed"
	DiffModified DiffKind = "modified"
)

type DiffChange struct {
	Kind       DiffKind `json:"kind"`
	EntityType string   `json:"entity_type"`
	EntityID   string   `json:"entity_id"`
	Path       string   `json:"path"`
	Before     any      `json:"before,omitempty"`
	After      any      `json:"after,omitempty"`
}

func Diff(from, to *Model) []DiffChange {
	var changes []DiffChange
	if from.Name != to.Name {
		changes = append(changes, DiffChange{Kind: DiffModified, EntityType: "model", EntityID: to.ID, Path: "name", Before: from.Name, After: to.Name})
	}
	if from.Description != to.Description {
		changes = append(changes, DiffChange{Kind: DiffModified, EntityType: "model", EntityID: to.ID, Path: "description", Before: from.Description, After: to.Description})
	}
	if !reflect.DeepEqual(from.Engines, to.Engines) {
		changes = append(changes, DiffChange{Kind: DiffModified, EntityType: "model", EntityID: to.ID, Path: "engines", Before: from.Engines, After: to.Engines})
	}

	changes = append(changes, diffSlice("frontend", from.Frontends, to.Frontends)...)
	changes = append(changes, diffSlice("backend", from.Backends, to.Backends)...)
	changes = append(changes, diffSlice("server", from.Servers, to.Servers)...)
	changes = append(changes, diffSlice("rule", from.Rules, to.Rules)...)
	changes = append(changes, diffSlice("tls_profile", from.TLSProfiles, to.TLSProfiles)...)
	changes = append(changes, diffSlice("health_check", from.HealthChecks, to.HealthChecks)...)
	changes = append(changes, diffSlice("rate_limit", from.RateLimits, to.RateLimits)...)
	changes = append(changes, diffSlice("cache", from.Caches, to.Caches)...)
	changes = append(changes, diffSlice("logger", from.Loggers, to.Loggers)...)
	changes = append(changes, diffSlice("opaque_block", from.OpaqueBlocks, to.OpaqueBlocks)...)

	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].EntityType != changes[j].EntityType {
			return changes[i].EntityType < changes[j].EntityType
		}
		if changes[i].EntityID != changes[j].EntityID {
			return changes[i].EntityID < changes[j].EntityID
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func diffSlice[T any](entityType string, from, to []T) []DiffChange {
	fromMap := byID(from)
	toMap := byID(to)
	ids := map[string]bool{}
	for id := range fromMap {
		ids[id] = true
	}
	for id := range toMap {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	var changes []DiffChange
	for _, id := range ordered {
		before, hadBefore := fromMap[id]
		after, hasAfter := toMap[id]
		switch {
		case !hadBefore && hasAfter:
			changes = append(changes, DiffChange{Kind: DiffAdded, EntityType: entityType, EntityID: id, Path: entityType + "." + id, After: after})
		case hadBefore && !hasAfter:
			changes = append(changes, DiffChange{Kind: DiffRemoved, EntityType: entityType, EntityID: id, Path: entityType + "." + id, Before: before})
		case hadBefore && hasAfter && !reflect.DeepEqual(before, after):
			changes = append(changes, DiffChange{Kind: DiffModified, EntityType: entityType, EntityID: id, Path: entityType + "." + id, Before: before, After: after})
		}
	}
	return changes
}

func byID[T any](items []T) map[string]T {
	out := make(map[string]T, len(items))
	for _, item := range items {
		id := entityID(item)
		if id != "" {
			out[id] = item
		}
	}
	return out
}

func entityID(item any) string {
	raw, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	var data struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	return data.ID
}

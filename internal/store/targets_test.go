package store

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
)

func TestTargetsAndClusters(t *testing.T) {
	st := New(t.TempDir())
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.UpsertTarget(t.Context(), meta.ID, Target{Name: "prod-a", Host: "lb1.example.com", Engine: ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if target.ID == "" || target.Port != 22 || target.User != "root" || target.ConfigPath != "/etc/haproxy/haproxy.cfg" {
		t.Fatalf("unexpected target defaults: %+v", target)
	}
	target.Name = "prod-a-renamed"
	target, err = st.UpsertTarget(t.Context(), meta.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := st.UpsertCluster(t.Context(), meta.ID, Cluster{Name: "prod", TargetIDs: []string{target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.ID == "" || cluster.Parallelism != 1 {
		t.Fatalf("unexpected cluster defaults: %+v", cluster)
	}
	all, err := st.ListTargets(t.Context(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Targets) != 1 || len(all.Clusters) != 1 {
		t.Fatalf("unexpected targets file: %+v", all)
	}
	if err := st.DeleteTarget(t.Context(), meta.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	all, err = st.ListTargets(t.Context(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Targets) != 0 || len(all.Clusters[0].TargetIDs) != 0 {
		t.Fatalf("target delete did not detach cluster refs: %+v", all)
	}
	if err := st.DeleteCluster(t.Context(), meta.ID, cluster.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = st.ListTargets(t.Context(), meta.ID)
	if len(all.Clusters) != 0 {
		t.Fatalf("cluster not deleted: %+v", all)
	}
}

func TestTargetErrors(t *testing.T) {
	st := New(t.TempDir())
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTarget(t.Context(), meta.ID, Target{}); err == nil {
		t.Fatal("expected missing name error")
	}
	if _, err := st.UpsertTarget(t.Context(), meta.ID, Target{Name: "x"}); err == nil {
		t.Fatal("expected missing host error")
	}
	if _, err := st.UpsertCluster(t.Context(), meta.ID, Cluster{}); err == nil {
		t.Fatal("expected missing cluster name error")
	}
	if _, err := st.UpsertCluster(t.Context(), meta.ID, Cluster{Name: "bad", TargetIDs: []string{"missing"}}); err == nil {
		t.Fatal("expected missing target reference error")
	}
	if err := st.DeleteTarget(t.Context(), meta.ID, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing target error, got %v", err)
	}
	if err := st.DeleteCluster(t.Context(), meta.ID, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing cluster error, got %v", err)
	}
	if _, err := st.ListTargets(t.Context(), "missing"); err == nil {
		t.Fatal("expected missing project error")
	}
}

func TestTargetDefaultsSortingAndCorruptFile(t *testing.T) {
	st := New(t.TempDir())
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineNginx})
	if err != nil {
		t.Fatal(err)
	}
	zTarget, err := st.UpsertTarget(t.Context(), meta.ID, Target{Name: "z-edge", Host: "z.example.com", Engine: ir.EngineNginx})
	if err != nil {
		t.Fatal(err)
	}
	if zTarget.ConfigPath != "/etc/nginx/nginx.conf" || zTarget.ReloadCommand != "systemctl reload nginx" {
		t.Fatalf("unexpected nginx defaults: %+v", zTarget)
	}
	createdAt := zTarget.CreatedAt
	zTarget.Host = "z2.example.com"
	zTarget.CreatedAt = time.Time{}
	zTarget, err = st.UpsertTarget(t.Context(), meta.ID, zTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !zTarget.CreatedAt.Equal(createdAt) {
		t.Fatalf("target created_at was not preserved: got %v want %v", zTarget.CreatedAt, createdAt)
	}
	aTarget, err := st.UpsertTarget(t.Context(), meta.ID, Target{Name: "a-edge", Host: "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	zCluster, err := st.UpsertCluster(t.Context(), meta.ID, Cluster{Name: "z-cluster", TargetIDs: []string{zTarget.ID}, Parallelism: 3, GateOnFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	clusterCreatedAt := zCluster.CreatedAt
	zCluster.TargetIDs = []string{zTarget.ID, aTarget.ID}
	zCluster.CreatedAt = time.Time{}
	zCluster, err = st.UpsertCluster(t.Context(), meta.ID, zCluster)
	if err != nil {
		t.Fatal(err)
	}
	if !zCluster.CreatedAt.Equal(clusterCreatedAt) {
		t.Fatalf("cluster created_at was not preserved: got %v want %v", zCluster.CreatedAt, clusterCreatedAt)
	}
	if _, err := st.UpsertCluster(t.Context(), meta.ID, Cluster{Name: "a-cluster", TargetIDs: []string{aTarget.ID}}); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListTargets(t.Context(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if all.Targets[0].Name != "a-edge" || all.Clusters[0].Name != "a-cluster" {
		t.Fatalf("targets and clusters were not sorted: %+v", all)
	}
	if got := removeString([]string{"a", "b", "a"}, "a"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("removeString mismatch: %v", got)
	}
	if err := os.WriteFile(st.targetsPath(meta.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListTargets(t.Context(), meta.ID); err == nil {
		t.Fatal("expected corrupt targets file error")
	}
}

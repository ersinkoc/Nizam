package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mizanproxy/mizan/internal/deploy"
	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/ir/parser"
	"github.com/mizanproxy/mizan/internal/server"
	"github.com/mizanproxy/mizan/internal/store"
	"github.com/mizanproxy/mizan/internal/validate"
	"github.com/mizanproxy/mizan/internal/version"
)

var listenAndServe = func(srv *http.Server) error {
	return srv.ListenAndServe()
}

var (
	listSnapshotsFromStore = func(st *store.Store, ctx context.Context, id string) ([]string, error) {
		return st.ListSnapshots(ctx, id)
	}
	listSnapshotTagsFromStore = func(st *store.Store, ctx context.Context, id string) ([]store.SnapshotTag, error) {
		return st.ListSnapshotTags(ctx, id)
	}
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return serve(ctx, nil, stdout, stderr)
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stdout, stderr)
	case "version":
		_, _ = fmt.Fprintf(stdout, "mizan %s (%s %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "project":
		return project(ctx, args[1:], stdout, stderr)
	case "snapshot":
		return snapshot(ctx, args[1:], stdout, stderr)
	case "target":
		return targetCmd(ctx, args[1:], stdout, stderr)
	case "cluster":
		return clusterCmd(ctx, args[1:], stdout, stderr)
	case "generate":
		return generate(ctx, args[1:], stdout, stderr)
	case "validate":
		return validateCmd(ctx, args[1:], stdout, stderr)
	case "deploy":
		return deployCmd(ctx, args[1:], stdout, stderr)
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bind := fs.String("bind", "127.0.0.1:7890", "address to bind")
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st := store.New(*home)
	if err := st.Bootstrap(ctx); err != nil {
		return err
	}
	srv := server.New(server.Config{Bind: *bind}, st, log)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	_, _ = fmt.Fprintf(stdout, "Mizan serving http://%s (data: %s)\n", *bind, st.Root())
	err := listenAndServe(srv)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	_, _ = fmt.Fprintf(stderr, "serve failed: %v\n", err)
	return err
}

func project(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan project new|list|delete|import")
		return errors.New("missing project command")
	}
	switch args[0] {
	case "new":
		fs := flag.NewFlagSet("project new", flag.ContinueOnError)
		fs.SetOutput(stderr)
		name := fs.String("name", "", "project name")
		desc := fs.String("description", "", "project description")
		engines := fs.String("engines", "haproxy", "comma-separated engines: haproxy,nginx")
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" && fs.NArg() > 0 {
			*name = fs.Arg(0)
		}
		if *name == "" {
			return errors.New("project name is required")
		}
		meta, _, version, err := store.New(*home).CreateProject(ctx, *name, *desc, parseEngines(*engines))
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"project": meta, "version": version})
	case "list":
		fs := flag.NewFlagSet("project list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		projects, err := store.New(*home).ListProjects(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(projects)
	case "delete":
		fs := flag.NewFlagSet("project delete", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("project id is required")
		}
		return store.New(*home).DeleteProject(ctx, fs.Arg(0))
	case "import":
		fs := flag.NewFlagSet("project import", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		name := fs.String("name", "", "project name")
		desc := fs.String("description", "", "project description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("config path is required")
		}
		path := fs.Arg(0)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		model, err := parser.ParseFile(path, data)
		if err != nil {
			return err
		}
		meta, _, version, err := store.New(*home).ImportProject(ctx, *name, *desc, model)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"project": meta, "version": version})
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func snapshot(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan snapshot list|get|revert|tag|tags")
		return errors.New("missing snapshot command")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("snapshot list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		snapshots, err := listSnapshotsFromStore(store.New(*home), ctx, *projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshots)
	case "get":
		fs := flag.NewFlagSet("snapshot get", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || fs.NArg() != 1 {
			return errors.New("--project and snapshot ref are required")
		}
		model, version, err := store.New(*home).GetSnapshot(ctx, *projectID, fs.Arg(0))
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"ir": model, "version": version})
	case "revert":
		fs := flag.NewFlagSet("snapshot revert", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		ifMatch := fs.String("if-match", "", "expected current version")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || fs.NArg() != 1 {
			return errors.New("--project and snapshot ref are required")
		}
		model, version, err := store.New(*home).RevertSnapshot(ctx, *projectID, fs.Arg(0), *ifMatch)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"ir": model, "version": version})
	case "tag":
		fs := flag.NewFlagSet("snapshot tag", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		label := fs.String("label", "", "tag label")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || *label == "" || fs.NArg() != 1 {
			return errors.New("--project, --label, and snapshot ref are required")
		}
		tag, err := store.New(*home).TagSnapshot(ctx, *projectID, fs.Arg(0), *label)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(tag)
	case "tags":
		fs := flag.NewFlagSet("snapshot tags", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		tags, err := listSnapshotTagsFromStore(store.New(*home), ctx, *projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(tags)
	default:
		return fmt.Errorf("unknown snapshot command %q", args[0])
	}
}

func targetCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan target list|add|delete")
		return errors.New("missing target command")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("target list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		targets, err := store.New(*home).ListTargets(ctx, *projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(targets.Targets)
	case "add":
		fs := flag.NewFlagSet("target add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		id := fs.String("id", "", "existing target id for update")
		name := fs.String("name", "", "target name")
		host := fs.String("host", "", "target host")
		port := fs.Int("port", 22, "SSH port")
		user := fs.String("user", "root", "SSH user")
		engine := fs.String("engine", "haproxy", "haproxy or nginx")
		configPath := fs.String("config-path", "", "remote config path")
		reloadCommand := fs.String("reload-command", "", "remote reload command")
		sudo := fs.Bool("sudo", false, "run install/reload through sudo")
		probe := fs.String("post-reload-probe", "", "optional HTTP probe URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		target, err := store.New(*home).UpsertTarget(ctx, *projectID, store.Target{
			ID:              *id,
			Name:            *name,
			Host:            *host,
			Port:            *port,
			User:            *user,
			Engine:          ir.Engine(*engine),
			ConfigPath:      *configPath,
			ReloadCommand:   *reloadCommand,
			Sudo:            *sudo,
			PostReloadProbe: *probe,
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(target)
	case "delete":
		fs := flag.NewFlagSet("target delete", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || fs.NArg() != 1 {
			return errors.New("--project and target id are required")
		}
		return store.New(*home).DeleteTarget(ctx, *projectID, fs.Arg(0))
	default:
		return fmt.Errorf("unknown target command %q", args[0])
	}
}

func clusterCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan cluster list|add|delete")
		return errors.New("missing cluster command")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("cluster list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		targets, err := store.New(*home).ListTargets(ctx, *projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(targets.Clusters)
	case "add":
		fs := flag.NewFlagSet("cluster add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		id := fs.String("id", "", "existing cluster id for update")
		name := fs.String("name", "", "cluster name")
		targetIDs := fs.String("target-ids", "", "comma-separated target ids")
		parallelism := fs.Int("parallelism", 1, "deployment parallelism")
		gate := fs.Bool("gate-on-failure", true, "stop rollout after the first failed target")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		cluster, err := store.New(*home).UpsertCluster(ctx, *projectID, store.Cluster{
			ID:            *id,
			Name:          *name,
			TargetIDs:     splitCSV(*targetIDs),
			Parallelism:   *parallelism,
			GateOnFailure: *gate,
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(cluster)
	case "delete":
		fs := flag.NewFlagSet("cluster delete", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || fs.NArg() != 1 {
			return errors.New("--project and cluster id are required")
		}
		return store.New(*home).DeleteCluster(ctx, *projectID, fs.Arg(0))
	default:
		return fmt.Errorf("unknown cluster command %q", args[0])
	}
}

func generate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	projectID := fs.String("project", "", "project id")
	target := fs.String("target", "haproxy", "haproxy or nginx")
	out := fs.String("out", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return errors.New("--project is required")
	}
	model, _, err := store.New(*home).GetIR(ctx, *projectID)
	if err != nil {
		return err
	}
	result, err := validate.Generate(model, ir.Engine(*target))
	if err != nil {
		return err
	}
	if *out != "" {
		return os.WriteFile(*out, []byte(result.Config), 0o644)
	}
	_, err = io.WriteString(stdout, result.Config)
	return err
}

func validateCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	projectID := fs.String("project", "", "project id")
	target := fs.String("target", "haproxy", "haproxy or nginx")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return errors.New("--project is required")
	}
	model, _, err := store.New(*home).GetIR(ctx, *projectID)
	if err != nil {
		return err
	}
	result, err := validate.Validate(ctx, model, ir.Engine(*target))
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func deployCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	projectID := fs.String("project", "", "project id")
	targetID := fs.String("target-id", "", "deployment target id")
	clusterID := fs.String("cluster-id", "", "deployment cluster id")
	execute := fs.Bool("execute", false, "execute remote SSH commands instead of dry-run planning")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return errors.New("--project is required")
	}
	if (*targetID == "") == (*clusterID == "") {
		return errors.New("exactly one of --target-id or --cluster-id is required")
	}
	st := store.New(*home)
	result, err := deploy.New().Run(ctx, st, deploy.Request{
		ProjectID: *projectID,
		TargetID:  *targetID,
		ClusterID: *clusterID,
		DryRun:    !*execute,
	})
	if err != nil {
		return err
	}
	_ = st.AppendAudit(ctx, store.AuditEvent{
		ProjectID:      *projectID,
		Actor:          "cli",
		Action:         "deploy.run",
		IRSnapshotHash: result.SnapshotHash,
		Outcome:        result.Status,
		Metadata: map[string]any{
			"dry_run":    result.DryRun,
			"target_id":  result.TargetID,
			"cluster_id": result.ClusterID,
			"steps":      len(result.Steps),
		},
	})
	return json.NewEncoder(stdout).Encode(result)
}

func parseEngines(v string) []ir.Engine {
	var engines []ir.Engine
	for _, part := range splitCSV(v) {
		switch strings.TrimSpace(part) {
		case "nginx":
			engines = append(engines, ir.EngineNginx)
		case "haproxy":
			engines = append(engines, ir.EngineHAProxy)
		}
	}
	return engines
}

func splitCSV(v string) []string {
	var items []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Mizan - visual config architect for HAProxy and Nginx

Usage:
  mizan serve [--bind 127.0.0.1:7890]
  mizan project new --name edge-prod --engines haproxy,nginx
  mizan project import ./haproxy.cfg --name imported-edge
  mizan project list
  mizan snapshot list --project <id>
  mizan target add --project <id> --name edge-01 --host 10.0.0.10
  mizan cluster add --project <id> --name prod --target-ids <target-id>
  mizan generate --project <id> --target haproxy [--out haproxy.cfg]
  mizan validate --project <id> --target nginx
  mizan deploy --project <id> --target-id <target-id>
  mizan version`)
}

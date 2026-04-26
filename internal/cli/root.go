package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mizanproxy/mizan/internal/deploy"
	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/ir/parser"
	"github.com/mizanproxy/mizan/internal/monitor"
	"github.com/mizanproxy/mizan/internal/secrets"
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

type projectExport struct {
	FormatVersion int               `json:"format_version"`
	ExportedAt    time.Time         `json:"exported_at"`
	Project       store.ProjectMeta `json:"project"`
	IR            *ir.Model         `json:"ir"`
	Version       string            `json:"version"`
	Targets       store.TargetsFile `json:"targets"`
}

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
	case "monitor":
		return monitorCmd(ctx, args[1:], stdout, stderr)
	case "audit":
		return auditCmd(ctx, args[1:], stdout, stderr)
	case "secret":
		return secretCmd(ctx, args[1:], stdout, stderr)
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type redactedSecret struct {
	Username      string `json:"username,omitempty"`
	HasPassword   bool   `json:"has_password"`
	HasPrivateKey bool   `json:"has_private_key"`
	HasPassphrase bool   `json:"has_passphrase"`
	HasToken      bool   `json:"has_token"`
}

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bind := fs.String("bind", "127.0.0.1:7890", "address to bind")
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	authToken := fs.String("auth-token", "", "bearer token required for HTTP access")
	authBasic := fs.String("auth-basic", "", "basic auth credential as user:password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	auth := server.AuthConfig{Token: firstNonEmpty(*authToken, os.Getenv("MIZAN_AUTH_TOKEN"))}
	basic := firstNonEmpty(*authBasic, os.Getenv("MIZAN_AUTH_BASIC"))
	if basic != "" {
		user, password, err := server.ParseBasicCredential(basic)
		if err != nil {
			return err
		}
		auth.BasicUser = user
		auth.BasicPassword = password
	}
	if server.RequiresAuth(*bind) && !auth.Enabled() {
		return errors.New("auth is required when binding outside localhost; set --auth-token, --auth-basic, MIZAN_AUTH_TOKEN, or MIZAN_AUTH_BASIC")
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st := store.New(*home)
	if err := st.Bootstrap(ctx); err != nil {
		return err
	}
	srv := server.New(server.Config{Bind: *bind, Auth: auth}, st, log)
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
		_, _ = fmt.Fprintln(stderr, "usage: mizan project new|list|delete|import|export")
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
	case "export":
		fs := flag.NewFlagSet("project export", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		out := fs.String("out", "", "write export JSON to file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("project id is required")
		}
		payload, err := exportProjectPayload(ctx, store.New(*home), fs.Arg(0))
		if err != nil {
			return err
		}
		if *out != "" {
			f, err := os.Create(*out)
			if err != nil {
				return err
			}
			defer f.Close()
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			return encoder.Encode(payload)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func exportProjectPayload(ctx context.Context, st *store.Store, id string) (projectExport, error) {
	meta, err := st.GetProject(ctx, id)
	if err != nil {
		return projectExport{}, err
	}
	model, version, err := st.GetIR(ctx, id)
	if err != nil {
		return projectExport{}, err
	}
	targets, err := st.ListTargets(ctx, id)
	if err != nil {
		return projectExport{}, err
	}
	return projectExport{
		FormatVersion: 1,
		ExportedAt:    time.Now().UTC(),
		Project:       meta,
		IR:            model,
		Version:       version,
		Targets:       targets,
	}, nil
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
		monitorEndpoint := fs.String("monitor-endpoint", "", "optional runtime monitor endpoint")
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
			MonitorEndpoint: *monitorEndpoint,
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
	vaultPassphrase := fs.String("vault-passphrase", "", "vault passphrase for target credentials")
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
	deployer := deploy.New()
	if *execute {
		deployer.Credentials = deployCredentialProvider(*home, vaultPassphraseBytes(*vaultPassphrase))
	}
	result, err := deployer.Run(ctx, st, deploy.Request{
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
			"dry_run":     result.DryRun,
			"target_id":   result.TargetID,
			"cluster_id":  result.ClusterID,
			"steps":       len(result.Steps),
			"credentials": deploy.CredentialSources(result.Steps),
		},
	})
	return json.NewEncoder(stdout).Encode(result)
}

func deployCredentialProvider(home string, passphrase []byte) deploy.CredentialProvider {
	if len(passphrase) == 0 {
		return nil
	}
	vault := secrets.New(secretsRoot(home))
	return func(ctx context.Context, target store.Target) (secrets.Secret, error) {
		secret, err := vault.Get(ctx, target.ID, passphrase)
		if errors.Is(err, os.ErrNotExist) {
			return secrets.Secret{}, nil
		}
		return secret, err
	}
}

func monitorCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan monitor snapshot|stream")
		return errors.New("missing monitor command")
	}
	switch args[0] {
	case "snapshot":
		fs := flag.NewFlagSet("monitor snapshot", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		snapshot, err := monitor.SnapshotTargets(ctx, store.New(*home), *projectID, nil)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshot)
	case "stream":
		fs := flag.NewFlagSet("monitor stream", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		limit := fs.Int("limit", 0, "number of snapshots to emit before exiting")
		interval := fs.Duration("interval", 5*time.Second, "delay between snapshots")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		if *limit < 0 {
			return errors.New("--limit must be non-negative")
		}
		if *interval <= 0 {
			return errors.New("--interval must be positive")
		}
		return streamSnapshots(ctx, store.New(*home), *projectID, *limit, *interval, stdout)
	default:
		return fmt.Errorf("unknown monitor command %q", args[0])
	}
}

func streamSnapshots(ctx context.Context, st *store.Store, projectID string, limit int, interval time.Duration, stdout io.Writer) error {
	encoder := json.NewEncoder(stdout)
	sent := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := monitor.SnapshotTargets(ctx, st, projectID, nil)
		if err != nil {
			return err
		}
		if err := encoder.Encode(snapshot); err != nil {
			return err
		}
		sent++
		if limit > 0 && sent >= limit {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func auditCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan audit show --project <id> [--csv] [--out audit.csv]")
		return errors.New("missing audit command")
	}
	switch args[0] {
	case "show":
		fs := flag.NewFlagSet("audit show", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		limit := fs.Int("limit", 100, "maximum events to return")
		from := fs.String("from", "", "RFC3339 start timestamp")
		to := fs.String("to", "", "RFC3339 end timestamp")
		actor := fs.String("actor", "", "actor filter")
		action := fs.String("action", "", "action filter")
		outcome := fs.String("outcome", "", "outcome filter")
		targetEngine := fs.String("target-engine", "", "target engine filter: haproxy or nginx")
		csvOut := fs.Bool("csv", false, "write CSV instead of JSON")
		out := fs.String("out", "", "write output to file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		filter, err := auditFilterFromFlags(*limit, *from, *to, *actor, *action, *outcome, *targetEngine)
		if err != nil {
			return err
		}
		events, err := store.New(*home).ListAuditFiltered(ctx, *projectID, filter)
		if err != nil {
			return err
		}
		writer := stdout
		var f *os.File
		if *out != "" {
			f, err = os.Create(*out)
			if err != nil {
				return err
			}
			defer f.Close()
			writer = f
		}
		if *csvOut || strings.HasSuffix(strings.ToLower(*out), ".csv") {
			return writeAuditCSV(writer, events)
		}
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(events)
	default:
		return fmt.Errorf("unknown audit command %q", args[0])
	}
}

func secretCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan secret set|get|list|delete")
		return errors.New("missing secret command")
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		id := fs.String("id", "", "secret id")
		vaultPassphrase := fs.String("vault-passphrase", "", "vault passphrase")
		username := fs.String("username", "", "SSH username")
		password := fs.String("password", "", "SSH password")
		privateKey := fs.String("private-key", "", "private key contents")
		privateKeyFile := fs.String("private-key-file", "", "path to private key file")
		secretPassphrase := fs.String("passphrase", "", "private key passphrase")
		token := fs.String("token", "", "API token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("--id is required")
		}
		if *privateKey != "" && *privateKeyFile != "" {
			return errors.New("use only one of --private-key or --private-key-file")
		}
		if *privateKeyFile != "" {
			data, err := os.ReadFile(*privateKeyFile)
			if err != nil {
				return err
			}
			*privateKey = string(data)
		}
		secret := secrets.Secret{
			Username:   *username,
			Password:   *password,
			PrivateKey: *privateKey,
			Passphrase: *secretPassphrase,
			Token:      *token,
		}
		if secret == (secrets.Secret{}) {
			return errors.New("at least one secret field is required")
		}
		if err := secrets.New(secretsRoot(*home)).Put(ctx, *id, vaultPassphraseBytes(*vaultPassphrase), secret); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]string{"id": *id, "status": "stored"})
	case "get":
		fs := flag.NewFlagSet("secret get", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		id := fs.String("id", "", "secret id")
		vaultPassphrase := fs.String("vault-passphrase", "", "vault passphrase")
		reveal := fs.Bool("reveal", false, "print secret values")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("--id is required")
		}
		secret, err := secrets.New(secretsRoot(*home)).Get(ctx, *id, vaultPassphraseBytes(*vaultPassphrase))
		if err != nil {
			return err
		}
		if *reveal {
			return json.NewEncoder(stdout).Encode(secret)
		}
		return json.NewEncoder(stdout).Encode(redactSecret(secret))
	case "list":
		fs := flag.NewFlagSet("secret list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ids, err := secrets.New(secretsRoot(*home)).List(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(ids)
	case "delete":
		fs := flag.NewFlagSet("secret delete", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		id := fs.String("id", "", "secret id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("--id is required")
		}
		return secrets.New(secretsRoot(*home)).Delete(ctx, *id)
	default:
		return fmt.Errorf("unknown secret command %q", args[0])
	}
}

func auditFilterFromFlags(limit int, from, to, actor, action, outcome, targetEngine string) (store.AuditFilter, error) {
	if limit < 1 {
		return store.AuditFilter{}, errors.New("--limit must be greater than zero")
	}
	filter := store.AuditFilter{Limit: limit, Actor: actor, Action: action, Outcome: outcome}
	if from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return filter, fmt.Errorf("invalid --from timestamp %q", from)
		}
		filter.From = parsed
	}
	if to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return filter, fmt.Errorf("invalid --to timestamp %q", to)
		}
		filter.To = parsed
	}
	if targetEngine != "" {
		engine := ir.Engine(targetEngine)
		if engine != ir.EngineHAProxy && engine != ir.EngineNginx {
			return filter, fmt.Errorf("invalid --target-engine %q", targetEngine)
		}
		filter.TargetEngine = engine
	}
	return filter, nil
}

func writeAuditCSV(w io.Writer, events []store.AuditEvent) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"event_id", "timestamp", "actor", "action", "outcome", "target_engine", "ir_snapshot_hash", "error_message"})
	for _, event := range events {
		_ = cw.Write([]string{
			event.EventID,
			event.Timestamp.UTC().Format(time.RFC3339),
			event.Actor,
			event.Action,
			event.Outcome,
			string(event.TargetEngine),
			event.IRSnapshotHash,
			event.ErrorMessage,
		})
	}
	cw.Flush()
	return cw.Error()
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func secretsRoot(home string) string {
	return filepath.Join(home, "secrets")
}

func vaultPassphraseBytes(value string) []byte {
	return []byte(firstNonEmpty(value, os.Getenv("MIZAN_VAULT_PASSPHRASE")))
}

func redactSecret(secret secrets.Secret) redactedSecret {
	return redactedSecret{
		Username:      secret.Username,
		HasPassword:   secret.Password != "",
		HasPrivateKey: secret.PrivateKey != "",
		HasPassphrase: secret.Passphrase != "",
		HasToken:      secret.Token != "",
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Mizan - visual config architect for HAProxy and Nginx

Usage:
  mizan serve [--bind 127.0.0.1:7890]
  mizan project new --name edge-prod --engines haproxy,nginx
  mizan project import ./haproxy.cfg --name imported-edge
  mizan project export <id> [--out mizan-export.json]
  mizan project list
  mizan snapshot list --project <id>
  mizan target add --project <id> --name edge-01 --host 10.0.0.10
  mizan cluster add --project <id> --name prod --target-ids <target-id>
  mizan generate --project <id> --target haproxy [--out haproxy.cfg]
  mizan validate --project <id> --target nginx
  mizan deploy --project <id> --target-id <target-id>
  mizan audit show --project <id> [--csv]
  mizan secret set --id <target-id> --username root --private-key-file ~/.ssh/id_ed25519
  mizan monitor snapshot --project <id>
  mizan monitor stream --project <id> [--limit 10]
  mizan version`)
}

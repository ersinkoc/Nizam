package cli

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mizanproxy/mizan/internal/deploy"
	"github.com/mizanproxy/mizan/internal/doctor"
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

const backupManifestPath = ".mizan-backup-manifest.json"
const backupManifestVersion = 2

type projectExport struct {
	FormatVersion int                     `json:"format_version"`
	ExportedAt    time.Time               `json:"exported_at"`
	Project       store.ProjectMeta       `json:"project"`
	IR            *ir.Model               `json:"ir"`
	Version       string                  `json:"version"`
	Targets       store.TargetsFile       `json:"targets"`
	Approvals     []store.ApprovalRequest `json:"approvals"`
}

type backupManifest struct {
	FormatVersion int          `json:"format_version"`
	CreatedAt     time.Time    `json:"created_at"`
	SourceRoot    string       `json:"source_root,omitempty"`
	Files         []backupFile `json:"files"`
}

type backupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return serve(ctx, nil, stdout, stderr)
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stdout, stderr)
	case "version":
		return versionCmd(args[1:], stdout, stderr)
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
	case "approval":
		return approvalCmd(ctx, args[1:], stdout, stderr)
	case "monitor":
		return monitorCmd(ctx, args[1:], stdout, stderr)
	case "audit":
		return auditCmd(ctx, args[1:], stdout, stderr)
	case "secret":
		return secretCmd(ctx, args[1:], stdout, stderr)
	case "backup":
		return backupCmd(ctx, args[1:], stdout, stderr)
	case "doctor":
		return doctorCmd(ctx, args[1:], stdout, stderr)
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func versionCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "write version metadata as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(stdout).Encode(map[string]string{
			"version": version.Version,
			"commit":  version.Commit,
			"date":    version.Date,
		})
	}
	_, _ = fmt.Fprintf(stdout, "mizan %s (%s %s)\n", version.Version, version.Commit, version.Date)
	return nil
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
	maxBodyBytes := fs.Int64("max-body-bytes", server.DefaultMaxBodyBytes, "maximum HTTP request body size in bytes")
	shutdownTimeout := fs.Duration("shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxBodyBytes <= 0 {
		return errors.New("--max-body-bytes must be greater than zero")
	}
	if *shutdownTimeout <= 0 {
		return errors.New("--shutdown-timeout must be positive")
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
	srv := server.New(server.Config{Bind: *bind, Auth: auth, MaxBodyBytes: *maxBodyBytes}, st, log)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
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
	approvals, err := st.ListApprovalRequests(ctx, id)
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
		Approvals:     approvals,
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
		rollbackCommand := fs.String("rollback-command", "", "optional remote rollback command after failed install/reload/probe")
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
			RollbackCommand: *rollbackCommand,
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
		requiredApprovals := fs.Int("required-approvals", 0, "distinct approval names required before cluster execute")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		cluster, err := store.New(*home).UpsertCluster(ctx, *projectID, store.Cluster{
			ID:                *id,
			Name:              *name,
			TargetIDs:         splitCSV(*targetIDs),
			Parallelism:       *parallelism,
			GateOnFailure:     *gate,
			RequiredApprovals: *requiredApprovals,
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

func approvalCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan approval list|request|approve")
		return errors.New("missing approval command")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("approval list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		requests, err := store.New(*home).ListApprovalRequests(ctx, *projectID)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(requests)
	case "request":
		fs := flag.NewFlagSet("approval request", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		targetID := fs.String("target-id", "", "deployment target id")
		clusterID := fs.String("cluster-id", "", "deployment cluster id")
		batch := fs.Int("batch", 0, "cluster batch number; 0 requests approval for all batches")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		if (*targetID == "") == (*clusterID == "") {
			return errors.New("exactly one of --target-id or --cluster-id is required")
		}
		if *batch < 0 {
			return errors.New("--batch must be non-negative")
		}
		st := store.New(*home)
		_, snapshot, err := st.GetIR(ctx, *projectID)
		if err != nil {
			return err
		}
		targets, err := st.ListTargets(ctx, *projectID)
		if err != nil {
			return err
		}
		requiredApprovals, err := approvalPolicyFromTargets(targets, *targetID, *clusterID)
		if err != nil {
			return err
		}
		request, err := st.CreateApprovalRequest(ctx, *projectID, store.ApprovalRequest{
			TargetID:          *targetID,
			ClusterID:         *clusterID,
			SnapshotHash:      snapshot,
			Batch:             *batch,
			RequiredApprovals: requiredApprovals,
		})
		if err != nil {
			return err
		}
		_ = st.AppendAudit(ctx, store.AuditEvent{
			ProjectID:      *projectID,
			Actor:          "cli",
			Action:         "approval.request",
			IRSnapshotHash: snapshot,
			Outcome:        "success",
			Metadata: map[string]any{
				"approval_request_id": request.ID,
				"target_id":           request.TargetID,
				"cluster_id":          request.ClusterID,
				"batch":               request.Batch,
				"required_approvals":  request.RequiredApprovals,
			},
		})
		return json.NewEncoder(stdout).Encode(request)
	case "approve":
		fs := flag.NewFlagSet("approval approve", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		projectID := fs.String("project", "", "project id")
		actor := fs.String("actor", "cli", "approval actor name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || fs.NArg() != 1 {
			return errors.New("--project and approval request id are required")
		}
		st := store.New(*home)
		request, err := st.ApproveRequest(ctx, *projectID, fs.Arg(0), *actor)
		if err != nil {
			return err
		}
		_ = st.AppendAudit(ctx, store.AuditEvent{
			ProjectID:      *projectID,
			Actor:          *actor,
			Action:         "approval.approve",
			IRSnapshotHash: request.SnapshotHash,
			Outcome:        "success",
			Metadata: map[string]any{
				"approval_request_id": request.ID,
				"status":              request.Status,
				"approvals":           len(request.Approvals),
				"required_approvals":  request.RequiredApprovals,
			},
		})
		return json.NewEncoder(stdout).Encode(request)
	default:
		return fmt.Errorf("unknown approval command %q", args[0])
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
	if len(args) > 0 && args[0] == "drill" {
		return deployDrillCmd(ctx, args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	projectID := fs.String("project", "", "project id")
	targetID := fs.String("target-id", "", "deployment target id")
	clusterID := fs.String("cluster-id", "", "deployment cluster id")
	approvalRequestID := fs.String("approval-request-id", "", "approval request id to use for snapshot-bound execution")
	execute := fs.Bool("execute", false, "execute remote SSH commands instead of dry-run planning")
	confirmSnapshot := fs.String("confirm-snapshot", "", "required snapshot hash when --execute is set")
	batch := fs.Int("batch", 0, "cluster batch number to deploy; 0 deploys all batches")
	approvedBy := fs.String("approved-by", "", "comma-separated approval names required by cluster policy")
	vaultPassphrase := fs.String("vault-passphrase", "", "vault passphrase for target credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return errors.New("--project is required")
	}
	if *approvalRequestID == "" && (*targetID == "") == (*clusterID == "") {
		return errors.New("exactly one of --target-id, --cluster-id, or --approval-request-id is required")
	}
	if *approvalRequestID == "" && *targetID != "" && *clusterID != "" {
		return errors.New("exactly one of --target-id, --cluster-id, or --approval-request-id is required")
	}
	if *execute && *confirmSnapshot == "" && *approvalRequestID == "" {
		return errors.New("--confirm-snapshot or --approval-request-id is required with --execute; run a dry run first and pass its snapshot_hash")
	}
	if *batch < 0 {
		return errors.New("--batch must be non-negative")
	}
	st := store.New(*home)
	approvedActors := splitCSV(*approvedBy)
	if *approvalRequestID != "" {
		request, err := st.GetApprovalRequest(ctx, *projectID, *approvalRequestID)
		if err != nil {
			return err
		}
		targetIDValue, clusterIDValue, batchValue, confirmSnapshotValue, actors, err := applyCLIApprovalRequest(*targetID, *clusterID, *batch, *confirmSnapshot, *execute, request)
		if err != nil {
			return err
		}
		*targetID = targetIDValue
		*clusterID = clusterIDValue
		*batch = batchValue
		*confirmSnapshot = confirmSnapshotValue
		approvedActors = append(approvedActors, actors...)
	}
	deployer := deploy.New()
	if *execute {
		deployer.Credentials = deployCredentialProvider(*home, vaultPassphraseBytes(*vaultPassphrase))
	}
	result, err := deployer.Run(ctx, st, deploy.Request{
		ProjectID:           *projectID,
		TargetID:            *targetID,
		ClusterID:           *clusterID,
		DryRun:              !*execute,
		ConfirmSnapshotHash: *confirmSnapshot,
		Batch:               *batch,
		ApprovedBy:          approvedActors,
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
			"dry_run":             result.DryRun,
			"target_id":           result.TargetID,
			"cluster_id":          result.ClusterID,
			"steps":               len(result.Steps),
			"batch":               result.Batch,
			"required_approvals":  result.RequiredApprovals,
			"approved_by":         result.ApprovedBy,
			"approval_request_id": *approvalRequestID,
			"rollback":            result.Rollback,
			"cleanup":             result.Cleanup,
			"credentials":         deploy.CredentialSources(result.Steps),
		},
	})
	return json.NewEncoder(stdout).Encode(result)
}

func deployDrillCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("deploy drill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	summary := fs.Bool("summary", false, "write compact drill summary JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("deploy drill does not accept positional arguments")
	}
	report := deploy.RunDrill(ctx)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if *summary {
		if err := encoder.Encode(deploy.SummarizeDrill(report)); err != nil {
			return err
		}
		if report.Status != "success" {
			return errors.New("deploy drill failed")
		}
		return nil
	}
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if report.Status != "success" {
		return errors.New("deploy drill failed")
	}
	return nil
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

func applyCLIApprovalRequest(targetID, clusterID string, batch int, confirmSnapshot string, execute bool, request store.ApprovalRequest) (string, string, int, string, []string, error) {
	if targetID != "" && targetID != request.TargetID {
		return "", "", 0, "", nil, errors.New("approval request target_id does not match deploy request")
	}
	if clusterID != "" && clusterID != request.ClusterID {
		return "", "", 0, "", nil, errors.New("approval request cluster_id does not match deploy request")
	}
	if batch != 0 && batch != request.Batch {
		return "", "", 0, "", nil, errors.New("approval request batch does not match deploy request")
	}
	if confirmSnapshot != "" && confirmSnapshot != request.SnapshotHash {
		return "", "", 0, "", nil, errors.New("approval request snapshot_hash does not match deploy request")
	}
	if execute && request.Status != store.ApprovalStatusApproved {
		return "", "", 0, "", nil, errors.New("approval request is not fully approved")
	}
	return request.TargetID, request.ClusterID, request.Batch, request.SnapshotHash, request.ApprovedActors(), nil
}

func approvalPolicyFromTargets(targets store.TargetsFile, targetID, clusterID string) (int, error) {
	if targetID == "" && clusterID == "" {
		return 0, errors.New("target_id or cluster_id is required")
	}
	if targetID != "" && clusterID != "" {
		return 0, errors.New("exactly one of target_id or cluster_id is required")
	}
	if targetID != "" {
		for _, target := range targets.Targets {
			if target.ID == targetID {
				return 0, nil
			}
		}
		return 0, errors.New("target not found")
	}
	for _, cluster := range targets.Clusters {
		if cluster.ID == clusterID {
			if cluster.RequiredApprovals < 0 {
				return 0, errors.New("cluster required approvals must be non-negative")
			}
			return cluster.RequiredApprovals, nil
		}
	}
	return 0, errors.New("cluster not found")
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
		actionPrefix := fs.String("action-prefix", "", "action prefix filter")
		outcome := fs.String("outcome", "", "outcome filter")
		targetEngine := fs.String("target-engine", "", "target engine filter: haproxy or nginx")
		targetID := fs.String("target-id", "", "metadata target id filter")
		clusterID := fs.String("cluster-id", "", "metadata cluster id filter")
		approvalRequestID := fs.String("approval-request-id", "", "metadata approval request id filter")
		batch := fs.Int("batch", 0, "metadata rollout batch filter")
		dryRun := fs.String("dry-run", "", "metadata dry-run filter: true or false")
		incident := fs.String("incident", "", "incident filter: true or false")
		rollbackFailed := fs.String("rollback-failed", "", "rollback failure filter: true or false")
		cleanupFailed := fs.String("cleanup-failed", "", "cleanup failure filter: true or false")
		csvOut := fs.Bool("csv", false, "write CSV instead of JSON")
		out := fs.String("out", "", "write output to file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return errors.New("--project is required")
		}
		filter, err := auditFilterFromFlags(*limit, *from, *to, *actor, *action, *actionPrefix, *outcome, *targetEngine, *targetID, *clusterID, *approvalRequestID, *batch, *dryRun, *incident, *rollbackFailed, *cleanupFailed)
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

func backupCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mizan backup create|inspect|restore")
		return errors.New("missing backup command")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		out := fs.String("out", "", "backup archive path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("--out is required")
		}
		manifest, err := createBackup(ctx, *home, *out)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(manifest)
	case "inspect":
		fs := flag.NewFlagSet("backup inspect", flag.ContinueOnError)
		fs.SetOutput(stderr)
		in := fs.String("in", "", "backup archive path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *in == "" {
			return errors.New("--in is required")
		}
		manifest, err := inspectBackup(*in)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(manifest)
	case "restore":
		fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
		fs.SetOutput(stderr)
		home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
		in := fs.String("in", "", "backup archive path")
		force := fs.Bool("force", false, "restore into a non-empty home directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *in == "" {
			return errors.New("--in is required")
		}
		manifest, err := restoreBackup(ctx, *home, *in, *force)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"restored": true, "manifest": manifest})
	default:
		return fmt.Errorf("unknown backup command %q", args[0])
	}
}

func createBackup(ctx context.Context, home, out string) (backupManifest, error) {
	if err := ctx.Err(); err != nil {
		return backupManifest{}, err
	}
	st := store.New(home)
	if err := st.Bootstrap(ctx); err != nil {
		return backupManifest{}, err
	}
	home = st.Root()
	info, err := os.Stat(home)
	if err != nil {
		return backupManifest{}, err
	}
	if !info.IsDir() {
		return backupManifest{}, fmt.Errorf("home %q is not a directory", home)
	}
	if inside, err := pathInside(home, out); err != nil {
		return backupManifest{}, err
	} else if inside {
		return backupManifest{}, errors.New("backup output must be outside the home directory")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return backupManifest{}, err
	}
	f, err := os.Create(out)
	if err != nil {
		return backupManifest{}, err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	manifest := backupManifest{
		FormatVersion: backupManifestVersion,
		CreatedAt:     time.Now().UTC(),
		SourceRoot:    home,
		Files:         []backupFile{},
	}
	if err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == home {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == backupManifestPath || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(w, hasher), src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		manifest.Files = append(manifest.Files, backupFile{
			Path:   rel,
			Size:   size,
			SHA256: hex.EncodeToString(hasher.Sum(nil)),
		})
		return nil
	}); err != nil {
		_ = zw.Close()
		return backupManifest{}, err
	}
	if err := writeBackupManifest(zw, manifest); err != nil {
		_ = zw.Close()
		return backupManifest{}, err
	}
	if err := zw.Close(); err != nil {
		return backupManifest{}, err
	}
	if err := f.Close(); err != nil {
		return backupManifest{}, err
	}
	return manifest, nil
}

func doctorCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", store.DefaultRoot(), "Mizan data directory")
	jsonOut := fs.Bool("json", false, "write machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report := doctor.Run(ctx, store.New(*home), nil)
	if *jsonOut {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "Mizan doctor: %s\n", report.Status)
		_, _ = fmt.Fprintf(stdout, "root: %s\n", report.Root)
		_, _ = fmt.Fprintf(stdout, "projects: %d, targets: %d, clusters: %d\n", report.ProjectCount, report.TargetCount, report.ClusterCount)
		for _, check := range report.Checks {
			_, _ = fmt.Fprintf(stdout, "- [%s] %s: %s\n", check.Status, check.Name, check.Message)
		}
	}
	if report.Status == doctor.StatusFail {
		return errors.New("doctor checks failed")
	}
	return nil
}

func inspectBackup(path string) (backupManifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return backupManifest{}, err
	}
	defer zr.Close()
	return readBackupManifest(zr.File)
}

func restoreBackup(ctx context.Context, home, in string, force bool) (backupManifest, error) {
	if err := ctx.Err(); err != nil {
		return backupManifest{}, err
	}
	home = store.New(home).Root()
	if err := ensureRestoreTarget(home, force); err != nil {
		return backupManifest{}, err
	}
	zr, err := zip.OpenReader(in)
	if err != nil {
		return backupManifest{}, err
	}
	defer zr.Close()
	manifest, err := readBackupManifest(zr.File)
	if err != nil {
		return backupManifest{}, err
	}
	if manifest.FormatVersion != backupManifestVersion {
		return backupManifest{}, fmt.Errorf("unsupported backup format version %d", manifest.FormatVersion)
	}
	expectedFiles, err := expectedBackupFiles(manifest)
	if err != nil {
		return backupManifest{}, err
	}
	if force {
		if err := os.RemoveAll(home); err != nil {
			return backupManifest{}, err
		}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return backupManifest{}, err
	}
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return backupManifest{}, err
		}
		if file.Name == backupManifestPath {
			continue
		}
		target, err := safeRestorePath(home, file.Name)
		if err != nil {
			return backupManifest{}, err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, file.Mode()); err != nil {
				return backupManifest{}, err
			}
			continue
		}
		expected, ok := expectedFiles[file.Name]
		if !ok {
			return backupManifest{}, fmt.Errorf("backup contains file not listed in manifest: %s", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return backupManifest{}, err
		}
		rc, err := file.Open()
		if err != nil {
			return backupManifest{}, err
		}
		size, hash, err := writeRestoredFile(target, file.Mode(), rc)
		if err != nil {
			_ = rc.Close()
			_ = os.Remove(target)
			return backupManifest{}, err
		}
		if err := rc.Close(); err != nil {
			_ = os.Remove(target)
			return backupManifest{}, err
		}
		if size != expected.Size || hash != expected.SHA256 {
			_ = os.Remove(target)
			return backupManifest{}, fmt.Errorf("backup integrity check failed for %s", file.Name)
		}
		delete(expectedFiles, file.Name)
	}
	for path := range expectedFiles {
		return backupManifest{}, fmt.Errorf("backup is missing manifest file %s", path)
	}
	return manifest, nil
}

func expectedBackupFiles(manifest backupManifest) (map[string]backupFile, error) {
	files := make(map[string]backupFile, len(manifest.Files))
	for _, file := range manifest.Files {
		if file.Path == "" {
			return nil, errors.New("backup manifest contains an empty file path")
		}
		if file.Size < 0 {
			return nil, fmt.Errorf("backup manifest contains negative size for %s", file.Path)
		}
		if len(file.SHA256) != sha256.Size*2 {
			return nil, fmt.Errorf("backup manifest contains invalid sha256 for %s", file.Path)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return nil, fmt.Errorf("backup manifest contains invalid sha256 for %s", file.Path)
		}
		file.SHA256 = strings.ToLower(file.SHA256)
		if _, exists := files[file.Path]; exists {
			return nil, fmt.Errorf("backup manifest contains duplicate file path %s", file.Path)
		}
		files[file.Path] = file
	}
	return files, nil
}

func ensureRestoreTarget(home string, force bool) error {
	entries, err := os.ReadDir(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("restore target %q is not empty; use --force to replace it", home)
	}
	return nil
}

func writeBackupManifest(zw *zip.Writer, manifest backupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	header := &zip.FileHeader{Name: backupManifestPath, Method: zip.Deflate}
	header.SetMode(0o644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func readBackupManifest(files []*zip.File) (backupManifest, error) {
	for _, file := range files {
		if file.Name != backupManifestPath {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return backupManifest{}, err
		}
		defer rc.Close()
		var manifest backupManifest
		if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
			return backupManifest{}, err
		}
		return manifest, nil
	}
	return backupManifest{}, errors.New("backup manifest is missing")
}

func safeRestorePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe backup path %q", name)
	}
	clean := pathCleanSlash(name)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("unsafe backup path %q", name)
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe backup path %q", name)
	}
	return target, nil
}

func pathCleanSlash(name string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
}

func writeRestoredFile(path string, mode os.FileMode, r io.Reader) (int64, string, error) {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hasher), r)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

func pathInside(root, candidate string) (bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func auditFilterFromFlags(limit int, from, to, actor, action, actionPrefix, outcome, targetEngine, targetID, clusterID, approvalRequestID string, batch int, dryRun, incident, rollbackFailed, cleanupFailed string) (store.AuditFilter, error) {
	if limit < 1 {
		return store.AuditFilter{}, errors.New("--limit must be greater than zero")
	}
	if batch < 0 {
		return store.AuditFilter{}, errors.New("--batch must be greater than zero")
	}
	filter := store.AuditFilter{
		Limit:             limit,
		Actor:             actor,
		Action:            action,
		ActionPrefix:      actionPrefix,
		Outcome:           outcome,
		TargetID:          targetID,
		ClusterID:         clusterID,
		ApprovalRequestID: approvalRequestID,
		Batch:             batch,
	}
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
	if dryRun != "" {
		parsed, err := strconv.ParseBool(dryRun)
		if err != nil {
			return filter, fmt.Errorf("invalid --dry-run %q", dryRun)
		}
		filter.DryRun = &parsed
	}
	if incident != "" {
		parsed, err := strconv.ParseBool(incident)
		if err != nil {
			return filter, fmt.Errorf("invalid --incident %q", incident)
		}
		filter.Incident = &parsed
	}
	if rollbackFailed != "" {
		parsed, err := strconv.ParseBool(rollbackFailed)
		if err != nil {
			return filter, fmt.Errorf("invalid --rollback-failed %q", rollbackFailed)
		}
		filter.RollbackFailed = &parsed
	}
	if cleanupFailed != "" {
		parsed, err := strconv.ParseBool(cleanupFailed)
		if err != nil {
			return filter, fmt.Errorf("invalid --cleanup-failed %q", cleanupFailed)
		}
		filter.CleanupFailed = &parsed
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
  mizan serve [--bind 127.0.0.1:7890] [--max-body-bytes 10485760]
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
  mizan deploy --project <id> --cluster-id <cluster-id> [--batch 1]
  mizan deploy --project <id> --cluster-id <cluster-id> --execute --confirm-snapshot <snapshot_hash> --approved-by alice,bob
  mizan deploy drill [--summary]
  mizan approval request --project <id> --cluster-id <cluster-id> [--batch 1]
  mizan approval approve --project <id> --actor alice <approval-request-id>
  mizan deploy --project <id> --approval-request-id <approval-request-id> --execute
  mizan audit show --project <id> [--csv]
  mizan secret set --id <target-id> --username root --private-key-file ~/.ssh/id_ed25519
  mizan backup create --out mizan-backup.zip
  mizan backup restore --in mizan-backup.zip --home /tmp/mizan-restore
  mizan doctor
  mizan monitor snapshot --project <id>
  mizan monitor stream --project <id> [--limit 10]
  mizan version [--json]`)
}

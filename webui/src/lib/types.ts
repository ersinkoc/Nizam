export type Engine = 'haproxy' | 'nginx';

export interface ProjectMeta {
  id: string;
  name: string;
  description?: string;
  engines: Engine[];
  created_at: string;
  updated_at: string;
}

export interface ProjectExport {
  format_version: number;
  exported_at: string;
  project: ProjectMeta;
  ir: Model;
  version: string;
  targets: TargetsResponse;
}

export interface Model {
  version: number;
  id: string;
  name: string;
  description?: string;
  engines: Engine[];
  frontends: Frontend[];
  backends: Backend[];
  servers: Server[];
  rules: Rule[];
  tls_profiles: TLSProfile[];
  health_checks: HealthCheck[];
  rate_limits: unknown[];
  caches: unknown[];
  loggers: unknown[];
  opaque_blocks?: unknown[];
  view: { zoom: number; pan: { x: number; y: number } };
}

export interface Frontend {
  id: string;
  name: string;
  bind: string;
  protocol: string;
  tls_id?: string;
  rules?: string[];
  default_backend?: string;
  view: { x: number; y: number };
}

export interface Backend {
  id: string;
  name: string;
  algorithm: string;
  health_check_id?: string;
  servers: string[];
  view: { x: number; y: number };
}

export interface Server {
  id: string;
  name?: string;
  address: string;
  port: number;
  weight: number;
  max_conn?: number;
}

export interface Rule {
  id: string;
  name?: string;
  predicate: { type: string; value: string };
  action: { type: string; backend_id?: string; location?: string; status?: number };
  view: { x: number; y: number };
}

export interface TLSProfile {
  id: string;
  name?: string;
  cert_path: string;
  key_path?: string;
  ciphers?: string;
  min_version?: string;
  alpn?: string[];
}

export interface HealthCheck {
  id: string;
  name?: string;
  type: string;
  path?: string;
  expected_status?: number[];
  interval_ms: number;
  timeout_ms: number;
  rise: number;
  fall: number;
}

export interface Issue {
  severity: 'error' | 'warning';
  entity_id?: string;
  field?: string;
  message: string;
}

export interface IRResponse {
  ir: Model;
  version: string;
  issues: Issue[];
}

export interface GenerateResult {
  target: Engine;
  config: string;
  source_map: { start_line: number; end_line: number; entity_id: string }[];
  warnings?: { entity_id?: string; message: string }[];
}

export interface NativeResult {
  available: boolean;
  skipped: boolean;
  command?: string;
  exit_code: number;
  stdout?: string;
  stderr?: string;
  error?: string;
}

export interface ValidateResult {
  target: Engine;
  issues: Issue[];
  generated: GenerateResult;
  native: NativeResult;
}

export interface SnapshotTag {
  label: string;
  ref: string;
  created_at: string;
}

export interface AuditEvent {
  event_id: string;
  project_id: string;
  timestamp: string;
  actor: string;
  action: string;
  ir_snapshot_hash?: string;
  target_engine?: Engine;
  outcome: string;
  error_message?: string;
  metadata?: Record<string, unknown>;
}

export interface AuditFilters {
  limit?: number;
  from?: string;
  to?: string;
  actor?: string;
  action?: string;
  outcome?: string;
  target_engine?: Engine | '';
}

export interface DiffChange {
  kind: 'added' | 'removed' | 'modified';
  entity_type: string;
  entity_id: string;
  path: string;
  before?: unknown;
  after?: unknown;
}

export interface DiffResponse {
  changes: DiffChange[];
}

export interface Target {
  id: string;
  name: string;
  host: string;
  port: number;
  user: string;
  engine: Engine;
  config_path: string;
  reload_command: string;
  sudo: boolean;
  post_reload_probe?: string;
  monitor_endpoint?: string;
  created_at: string;
  updated_at: string;
}

export interface Cluster {
  id: string;
  name: string;
  target_ids: string[];
  parallelism: number;
  gate_on_failure: boolean;
  created_at: string;
  updated_at: string;
}

export interface TargetsResponse {
  targets: Target[];
  clusters: Cluster[];
}

export interface DeployStep {
  target_id: string;
  target_name: string;
  engine: Engine;
  stage: string;
  status: 'success' | 'failed' | 'skipped';
  command?: string;
  message?: string;
  credential_source?: 'vault' | 'local_ssh';
  batch: number;
}

export interface DeployResult {
  project_id: string;
  target_id?: string;
  cluster_id?: string;
  snapshot_hash: string;
  dry_run: boolean;
  status: 'success' | 'failed';
  started_at: string;
  finished_at: string;
  steps: DeployStep[];
}

export interface ProbeResult {
  target_id: string;
  target_name: string;
  url: string;
  status: 'success' | 'failed';
  message?: string;
  checked_at: string;
}

export interface MonitorSummary {
  total_targets: number;
  healthy: number;
  warning: number;
  unknown: number;
  failed: number;
}

export interface MonitorTarget {
  target_id: string;
  name: string;
  host: string;
  engine: Engine;
  status: 'healthy' | 'warning' | 'unknown' | 'failed';
  message: string;
}

export interface MonitorSnapshot {
  project_id: string;
  generated_at: string;
  summary: MonitorSummary;
  targets: MonitorTarget[];
}

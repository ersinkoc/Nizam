import {
  Activity,
  BookOpenText,
  Boxes,
  CheckCircle2,
  Code2,
  Download,
  History,
  LineChart,
  Moon,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Server,
  ShieldCheck,
  Sun,
  Tag,
  Trash2,
  TriangleAlert,
  UploadCloud
} from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from './api/client';
import { TopologyCanvas } from './components/topology/TopologyCanvas';
import { connectEntities, moveEntity } from './lib/ir-mutations';
import type {
  AuditEvent,
  AuditFilters,
  DeployResult,
  DiffChange,
  Engine,
  GenerateResult,
  IRResponse,
  Model,
  MonitorSnapshot,
  NativeResult,
  ProbeResult,
  ProjectMeta,
  TargetsResponse,
  ValidateResult
} from './lib/types';

const samplePatch = (model: Model): Model => ({
  ...model,
  frontends: [
    {
      id: 'fe_web',
      name: 'web',
      bind: ':443',
      protocol: 'http',
      tls_id: 'tls_default',
      rules: ['r_api'],
      default_backend: 'be_app',
      view: { x: 80, y: 100 }
    }
  ],
  backends: [
    {
      id: 'be_app',
      name: 'app-pool',
      algorithm: 'leastconn',
      health_check_id: 'hc_default',
      servers: ['s_app_1', 's_app_2'],
      view: { x: 420, y: 120 }
    },
    {
      id: 'be_api',
      name: 'api-pool',
      algorithm: 'roundrobin',
      servers: ['s_api_1'],
      view: { x: 420, y: 260 }
    }
  ],
  servers: [
    { id: 's_app_1', address: '10.0.1.10', port: 8080, weight: 100, max_conn: 1024 },
    { id: 's_app_2', address: '10.0.1.11', port: 8080, weight: 100, max_conn: 1024 },
    { id: 's_api_1', address: '10.0.2.20', port: 9000, weight: 100 }
  ],
  rules: [
    {
      id: 'r_api',
      predicate: { type: 'path_prefix', value: '/api/' },
      action: { type: 'use_backend', backend_id: 'be_api' },
      view: { x: 250, y: 180 }
    }
  ],
  tls_profiles: [
    {
      id: 'tls_default',
      cert_path: '/etc/mizan/certs/edge.pem',
      key_path: '/etc/mizan/certs/edge.key',
      min_version: 'TLSv1.2',
      alpn: ['h2', 'http/1.1']
    }
  ],
  health_checks: [
    {
      id: 'hc_default',
      type: 'http',
      path: '/healthz',
      expected_status: [200],
      interval_ms: 2000,
      timeout_ms: 1000,
      rise: 2,
      fall: 3
    }
  ]
});

const defaultAuditFilters: AuditFilters = {
  actor: '',
  action: '',
  outcome: '',
  target_engine: ''
};

export function App() {
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  const [active, setActive] = useState<ProjectMeta | null>(null);
  const [irResponse, setIRResponse] = useState<IRResponse | null>(null);
  const [draft, setDraft] = useState('');
  const [generated, setGenerated] = useState<GenerateResult | null>(null);
  const [validation, setValidation] = useState<ValidateResult | null>(null);
  const [snapshots, setSnapshots] = useState<string[]>([]);
  const [tags, setTags] = useState<{ label: string; ref: string }[]>([]);
  const [diffChanges, setDiffChanges] = useState<DiffChange[]>([]);
  const [audit, setAudit] = useState<AuditEvent[]>([]);
  const [auditFilters, setAuditFilters] = useState<AuditFilters>(defaultAuditFilters);
  const auditFiltersRef = useRef<AuditFilters>(defaultAuditFilters);
  const [targetsFile, setTargetsFile] = useState<TargetsResponse>({ targets: [], clusters: [] });
  const [deployResult, setDeployResult] = useState<DeployResult | null>(null);
  const [probeResult, setProbeResult] = useState<ProbeResult | null>(null);
  const [monitorSnapshot, setMonitorSnapshot] = useState<MonitorSnapshot | null>(null);
  const [monitorStream, setMonitorStream] = useState<'idle' | 'connecting' | 'live' | 'error'>('idle');
  const [auditStream, setAuditStream] = useState<'idle' | 'connecting' | 'live' | 'error'>('idle');
  const [target, setTarget] = useState<Engine>('haproxy');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [theme, setTheme] = useState(() => localStorage.getItem('mizan-theme') ?? 'dark');

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('mizan-theme', theme);
  }, [theme]);

  useEffect(() => {
    void reloadProjects();
  }, []);

  useEffect(() => {
    auditFiltersRef.current = auditFilters;
  }, [auditFilters]);

  useEffect(() => {
    if (!active) return;
    setGenerated(null);
    setValidation(null);
    setDiffChanges([]);
    setAudit([]);
    setAuditFilters(defaultAuditFilters);
    setTargetsFile({ targets: [], clusters: [] });
    setDeployResult(null);
    setProbeResult(null);
    setMonitorSnapshot(null);
    setError('');
    api
      .getIR(active.id)
      .then((res) => {
        setIRResponse(res);
        setDraft(JSON.stringify(res.ir, null, 2));
        void reloadSnapshots(active.id);
        void reloadAudit(active.id, defaultAuditFilters);
        void reloadTargets(active.id);
        void reloadMonitor(active.id);
      })
      .catch((err: Error) => setError(err.message));
  }, [active]);

  useEffect(() => {
    if (!active || typeof EventSource === 'undefined') {
      setMonitorStream('idle');
      return;
    }
    setMonitorStream('connecting');
    const source = new EventSource(api.monitorStreamURL(active.id));
    const onSnapshot = (event: Event) => {
      try {
        setMonitorSnapshot(JSON.parse((event as MessageEvent<string>).data) as MonitorSnapshot);
        setMonitorStream('live');
      } catch (err) {
        setMonitorStream('error');
      }
    };
    source.addEventListener('snapshot', onSnapshot);
    source.onerror = () => setMonitorStream('error');
    return () => {
      source.removeEventListener('snapshot', onSnapshot);
      source.close();
    };
  }, [active?.id]);

  useEffect(() => {
    if (!active || typeof EventSource === 'undefined') {
      setAuditStream('idle');
      return;
    }
    setAuditStream('connecting');
    const source = new EventSource(api.projectEventsURL(active.id));
    const onAudit = (event: Event) => {
      try {
        const item = JSON.parse((event as MessageEvent<string>).data) as AuditEvent;
        if (auditMatchesFilters(item, auditFiltersRef.current)) {
          setAudit((items) => [item, ...items.filter((existing) => existing.event_id !== item.event_id)].slice(0, 50));
        }
        setAuditStream('live');
      } catch (err) {
        setAuditStream('error');
      }
    };
    source.addEventListener('audit', onAudit);
    source.onerror = () => setAuditStream('error');
    return () => {
      source.removeEventListener('audit', onAudit);
      source.close();
    };
  }, [active?.id]);

  const model = irResponse?.ir ?? null;
  const counts = useMemo(
    () => ({
      frontends: model?.frontends.length ?? 0,
      backends: model?.backends.length ?? 0,
      servers: model?.servers.length ?? 0,
      rules: model?.rules.length ?? 0
    }),
    [model]
  );

  async function reloadProjects() {
    setBusy(true);
    try {
      const list = await api.listProjects();
      setProjects(list);
      setActive((current) => current ?? list[0] ?? null);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function createProject(formData: FormData) {
    setBusy(true);
    setError('');
    try {
      const engines = formData.getAll('engine') as Engine[];
      const created = await api.createProject({
        name: String(formData.get('name') || 'edge-prod'),
        description: String(formData.get('description') || ''),
        engines: engines.length ? engines : ['haproxy']
      });
      setProjects((items) => [created.project, ...items]);
      setActive(created.project);
      setIRResponse({ ir: created.ir, version: created.version, issues: [] });
      setDraft(JSON.stringify(created.ir, null, 2));
      await reloadSnapshots(created.project.id);
      await reloadAudit(created.project.id);
      await reloadTargets(created.project.id);
      await reloadMonitor(created.project.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function importProject(formData: FormData) {
    setBusy(true);
    setError('');
    try {
      const imported = await api.importProject({
        name: String(formData.get('name') || 'imported-config'),
        description: String(formData.get('description') || ''),
        filename: String(formData.get('filename') || 'haproxy.cfg'),
        config: String(formData.get('config') || '')
      });
      setProjects((items) => [imported.project, ...items]);
      setActive(imported.project);
      setIRResponse({ ir: imported.ir, version: imported.version, issues: imported.issues ?? [] });
      setDraft(JSON.stringify(imported.ir, null, 2));
      await reloadSnapshots(imported.project.id);
      await reloadAudit(imported.project.id);
      await reloadTargets(imported.project.id);
      await reloadMonitor(imported.project.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function saveDraft(nextModel?: Model) {
    if (!active || !irResponse) return;
    setBusy(true);
    setError('');
    try {
      const parsed = nextModel ?? (JSON.parse(draft) as Model);
      const saved = await api.saveIR(active.id, parsed, irResponse.version);
      setIRResponse(saved);
      setDraft(JSON.stringify(saved.ir, null, 2));
      await reloadSnapshots(active.id);
      await reloadAudit(active.id);
      setDiffChanges([]);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function reloadSnapshots(projectID = active?.id ?? '') {
    if (!projectID) return;
    const [snapshotList, tagList] = await Promise.all([api.listSnapshots(projectID), api.listTags(projectID)]);
    setSnapshots(snapshotList);
    setTags(tagList);
  }

  async function reloadAudit(projectID = active?.id ?? '', filters = auditFilters) {
    if (!projectID) return;
    setAudit(await api.listAudit(projectID, { ...filters, limit: 50 }));
  }

  async function applyAuditFilters(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await reloadAudit();
  }

  async function clearAuditFilters() {
    setAuditFilters(defaultAuditFilters);
    if (active) {
      setAudit(await api.listAudit(active.id, { ...defaultAuditFilters, limit: 50 }));
    }
  }

  function exportAuditCSV() {
    if (!active) return;
    const link = document.createElement('a');
    link.href = api.auditCSVURL(active.id, { ...auditFilters, limit: 1000 });
    link.download = `${active.name.replace(/[^a-z0-9_-]+/gi, '-').replace(/^-+|-+$/g, '') || active.id}-audit.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  }

  async function reloadTargets(projectID = active?.id ?? '') {
    if (!projectID) return;
    setTargetsFile(await api.listTargets(projectID));
  }

  async function reloadMonitor(projectID = active?.id ?? '') {
    if (!projectID) return;
    setMonitorSnapshot(await api.monitorSnapshot(projectID));
  }

  async function upsertTarget(formData: FormData) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      await api.upsertTarget(active.id, {
        name: String(formData.get('name') || ''),
        host: String(formData.get('host') || ''),
        port: Number(formData.get('port') || 22),
        user: String(formData.get('user') || 'root'),
        engine: String(formData.get('engine') || 'haproxy') as Engine,
        config_path: String(formData.get('config_path') || ''),
        reload_command: String(formData.get('reload_command') || ''),
        sudo: formData.get('sudo') === 'on',
        post_reload_probe: String(formData.get('post_reload_probe') || ''),
        monitor_endpoint: String(formData.get('monitor_endpoint') || '')
      });
      await reloadTargets(active.id);
      await reloadMonitor(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function deleteTarget(targetID: string) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      await api.deleteTarget(active.id, targetID);
      await reloadTargets(active.id);
      await reloadMonitor(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function upsertCluster(formData: FormData) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      await api.upsertCluster(active.id, {
        name: String(formData.get('name') || ''),
        target_ids: formData.getAll('target_id').map(String),
        parallelism: Number(formData.get('parallelism') || 1),
        gate_on_failure: formData.get('gate_on_failure') === 'on'
      });
      await reloadTargets(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function deleteCluster(clusterID: string) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      await api.deleteCluster(active.id, clusterID);
      await reloadTargets(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function previewDeployTarget(targetID: string) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      const result = await api.deploy(active.id, { target_id: targetID, dry_run: true });
      setDeployResult(result);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function probeTarget(targetID: string) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      const result = await api.probeTarget(active.id, targetID);
      setProbeResult(result);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function previewDeployCluster(clusterID: string) {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      const result = await api.deploy(active.id, { cluster_id: clusterID, dry_run: true });
      setDeployResult(result);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function tagLatest(formData: FormData) {
    if (!active || !snapshots[0]) return;
    setBusy(true);
    setError('');
    try {
      await api.tagSnapshot(active.id, snapshots[0], String(formData.get('label') || 'release'));
      await reloadSnapshots(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function revertTo(ref: string) {
    if (!active || !irResponse) return;
    setBusy(true);
    setError('');
    try {
      const reverted = await api.revertSnapshot(active.id, ref, irResponse.version);
      setIRResponse(reverted);
      setDraft(JSON.stringify(reverted.ir, null, 2));
      await reloadSnapshots(active.id);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function compareLatestSnapshots() {
    if (!active || snapshots.length < 2) return;
    setBusy(true);
    setError('');
    try {
      const result = await api.diffSnapshots(active.id, snapshots[1], snapshots[0]);
      setDiffChanges(result.changes);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function handleMoveEntity(entityID: string, x: number, y: number) {
    if (!model) return;
    const result = moveEntity(model, entityID, x, y);
    if (result.changed) {
      void saveDraft(result.model);
    }
  }

  function handleConnectEntities(sourceID: string, targetID: string) {
    if (!model) return;
    const result = connectEntities(model, sourceID, targetID);
    if (result.changed) {
      void saveDraft(result.model);
    }
  }

  async function generateConfig() {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      setGenerated(await api.generate(active.id, target));
      setValidation(null);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function validateConfig() {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      const result = await api.validate(active.id, target);
      setValidation(result);
      setGenerated(result.generated);
      if (result.issues.length) {
        setIRResponse((current) => (current ? { ...current, issues: result.issues } : current));
      }
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function exportActiveProject() {
    if (!active) return;
    setBusy(true);
    setError('');
    try {
      const payload = await api.exportProject(active.id);
      const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `${active.name.replace(/[^a-z0-9_-]+/gi, '-').replace(/^-+|-+$/g, '') || active.id}-mizan-export.json`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
      await reloadAudit(active.id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <Boxes aria-hidden="true" />
          <div>
            <strong>Mizan</strong>
            <span>Config Architect</span>
          </div>
        </div>
        <form action={createProject} className="new-project">
          <input name="name" placeholder="Project name" aria-label="Project name" />
          <input name="description" placeholder="Description" aria-label="Description" />
          <label><input type="checkbox" name="engine" value="haproxy" defaultChecked /> HAProxy</label>
          <label><input type="checkbox" name="engine" value="nginx" /> Nginx</label>
          <button type="submit" disabled={busy}>
            <Plus size={16} /> New
          </button>
        </form>
        <div className="project-list">
          {projects.map((project) => (
            <button
              key={project.id}
              className={project.id === active?.id ? 'project active' : 'project'}
              onClick={() => setActive(project)}
            >
              <span>{project.name}</span>
              <small>{project.engines.join(' + ')}</small>
            </button>
          ))}
        </div>
        <form action={importProject} className="import-project">
          <strong><Code2 size={15} /> Import</strong>
          <input name="name" placeholder="Imported project" aria-label="Imported project name" />
          <input name="filename" placeholder="haproxy.cfg or nginx.conf" aria-label="Config filename" />
          <textarea name="config" placeholder="Paste config" aria-label="Config text" rows={7} />
          <button type="submit" disabled={busy}>
            <Plus size={16} /> Import
          </button>
        </form>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <div>
            <h1>{active?.name ?? 'No project selected'}</h1>
            <p>{active?.description || 'Create a project to start designing a load-balancer config.'}</p>
          </div>
          <div className="toolbar">
            <button onClick={reloadProjects} disabled={busy} title="Refresh projects"><RefreshCw size={16} /></button>
            <button onClick={exportActiveProject} disabled={!active || busy} title="Export project"><Download size={16} /></button>
            <button onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} title="Toggle theme">
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </button>
          </div>
        </header>

        {error && <div className="alert"><TriangleAlert size={16} /> {error}</div>}

        <div className="metrics">
          <Metric icon={<Activity />} label="Frontends" value={counts.frontends} />
          <Metric icon={<Boxes />} label="Backends" value={counts.backends} />
          <Metric icon={<Server />} label="Servers" value={counts.servers} />
          <Metric icon={<CheckCircle2 />} label="Rules" value={counts.rules} />
        </div>

        <div className="split">
          <section className="panel editor">
            <div className="panel-head">
              <h2>IR Editor</h2>
              <div className="toolbar">
                <button disabled={!model || busy} onClick={() => model && saveDraft(samplePatch(model))}>
                  <Plus size={16} /> Sample
                </button>
                <button disabled={!model || busy} onClick={() => saveDraft()}>
                  <Save size={16} /> Save
                </button>
              </div>
            </div>
            <textarea value={draft} onChange={(event) => setDraft(event.target.value)} spellCheck={false} />
          </section>

          <section className="panel">
            <div className="panel-head">
              <h2>Generate</h2>
              <div className="toolbar segmented">
                <button className={target === 'haproxy' ? 'selected' : ''} onClick={() => setTarget('haproxy')}>HAProxy</button>
                <button className={target === 'nginx' ? 'selected' : ''} onClick={() => setTarget('nginx')}>Nginx</button>
                <button disabled={!active || busy} onClick={generateConfig}><Code2 size={16} /> Run</button>
                <button disabled={!active || busy} onClick={validateConfig}><ShieldCheck size={16} /> Validate</button>
              </div>
            </div>
            <IssueList issues={irResponse?.issues ?? []} />
            {validation && <NativeStatus native={validation.native} />}
            <ConfigPreview config={generated?.config ?? ''} />
          </section>
        </div>

        <section className="panel topology-panel">
          <div className="panel-head">
            <h2>Topology</h2>
            <span className="panel-note">Derived from the active IR</span>
          </div>
          <TopologyCanvas
            model={model}
            issues={irResponse?.issues ?? []}
            onMoveEntity={handleMoveEntity}
            onConnectEntities={handleConnectEntities}
          />
        </section>

        <section className="panel targets-panel">
          <div className="panel-head">
            <h2><Server size={16} /> Deployment Targets</h2>
            <button disabled={!active || busy} onClick={() => reloadTargets()}>
              <RefreshCw size={16} /> Refresh
            </button>
          </div>
          <div className="targets-grid">
            <form action={upsertTarget} className="target-form">
              <h3>Target</h3>
              <input name="name" placeholder="edge-01" aria-label="Target name" />
              <input name="host" placeholder="10.0.0.10" aria-label="Target host" />
              <div className="target-form-row">
                <input name="user" placeholder="root" aria-label="SSH user" />
                <input name="port" type="number" min="1" max="65535" placeholder="22" aria-label="SSH port" />
              </div>
              <select name="engine" aria-label="Target engine" defaultValue="haproxy">
                <option value="haproxy">HAProxy</option>
                <option value="nginx">Nginx</option>
              </select>
              <input name="config_path" placeholder="/etc/haproxy/haproxy.cfg" aria-label="Remote config path" />
              <input name="reload_command" placeholder="systemctl reload haproxy" aria-label="Reload command" />
              <input name="post_reload_probe" placeholder="https://edge.example.com/healthz" aria-label="Post reload probe" />
              <input name="monitor_endpoint" placeholder="https://edge.example.com/haproxy?stats;csv" aria-label="Monitor endpoint" />
              <label className="check-line"><input type="checkbox" name="sudo" /> Use sudo</label>
              <button type="submit" disabled={!active || busy}><Plus size={16} /> Add Target</button>
            </form>

            <div className="target-list">
              <h3>Targets</h3>
              {targetsFile.targets.length ? targetsFile.targets.map((item) => (
                <article key={item.id} className="target-card">
                  <div>
                    <strong>{item.name}</strong>
                    <span>{item.user}@{item.host}:{item.port}</span>
                  </div>
                  <small>{item.engine} to {item.config_path}</small>
                  {item.monitor_endpoint && <small>monitor {item.monitor_endpoint}</small>}
                  <code>{item.reload_command}</code>
                  <div className="target-card-actions">
                    <button onClick={() => probeTarget(item.id)} disabled={busy} title="Test target probe">
                      <ShieldCheck size={15} />
                    </button>
                    <button onClick={() => previewDeployTarget(item.id)} disabled={busy} title="Preview deployment">
                      <UploadCloud size={15} />
                    </button>
                    <button onClick={() => deleteTarget(item.id)} disabled={busy} title="Delete target">
                      <Trash2 size={15} />
                    </button>
                  </div>
                </article>
              )) : <p className="muted">No deployment targets yet.</p>}
            </div>

            <form action={upsertCluster} className="cluster-form">
              <h3>Cluster</h3>
              <input name="name" placeholder="production-edge" aria-label="Cluster name" />
              <input name="parallelism" type="number" min="1" placeholder="1" aria-label="Deployment parallelism" />
              <label className="check-line"><input type="checkbox" name="gate_on_failure" defaultChecked /> Gate on failure</label>
              <div className="cluster-targets">
                {targetsFile.targets.map((item) => (
                  <label key={item.id} className="check-line">
                    <input type="checkbox" name="target_id" value={item.id} />
                    {item.name}
                  </label>
                ))}
              </div>
              <button type="submit" disabled={!active || busy || !targetsFile.targets.length}><Plus size={16} /> Add Cluster</button>
            </form>

            <div className="cluster-list">
              <h3>Clusters</h3>
              {targetsFile.clusters.length ? targetsFile.clusters.map((item) => (
                <article key={item.id} className="cluster-card">
                  <div>
                    <strong>{item.name}</strong>
                    <span>{item.target_ids.length} target(s), parallelism {item.parallelism}</span>
                  </div>
                  <small>{item.gate_on_failure ? 'Stops on first failed deployment' : 'Continues after failures'}</small>
                  <div className="target-card-actions">
                    <button onClick={() => previewDeployCluster(item.id)} disabled={busy} title="Preview deployment">
                      <UploadCloud size={15} />
                    </button>
                    <button onClick={() => deleteCluster(item.id)} disabled={busy} title="Delete cluster">
                      <Trash2 size={15} />
                    </button>
                  </div>
                </article>
              )) : <p className="muted">No clusters yet.</p>}
            </div>
          </div>
          <ProbeStatus result={probeResult} />
          <DeployPlan result={deployResult} />
        </section>

        <section className="panel monitor-panel">
          <div className="panel-head">
            <h2><LineChart size={16} /> Monitor</h2>
            <button disabled={!active || busy} onClick={() => reloadMonitor()}>
              <RefreshCw size={16} /> Refresh
            </button>
          </div>
          <MonitorPanel snapshot={monitorSnapshot} stream={monitorStream} />
        </section>

        <section className="panel snapshots">
          <div className="panel-head">
            <h2><History size={16} /> Snapshots</h2>
            <div className="snapshot-actions">
              <button disabled={snapshots.length < 2 || busy} onClick={compareLatestSnapshots}>
                <Code2 size={16} /> Diff
              </button>
              <form action={tagLatest} className="tag-form">
                <input name="label" placeholder="Tag latest" aria-label="Snapshot tag label" />
                <button disabled={!snapshots.length || busy}><Tag size={16} /> Tag</button>
              </form>
            </div>
          </div>
          <DiffList changes={diffChanges} />
          <div className="snapshot-grid">
            <div>
              <h3>History</h3>
              {snapshots.length ? snapshots.slice(0, 8).map((snapshot) => (
                <button key={snapshot} className="snapshot-row" onClick={() => revertTo(snapshot)} disabled={busy}>
                  <span>{snapshot}</span>
                  <RotateCcw size={15} />
                </button>
              )) : <p>No snapshots yet.</p>}
            </div>
            <div>
              <h3>Tags</h3>
              {tags.length ? tags.map((item) => (
                <button key={item.label} className="snapshot-row" onClick={() => revertTo(item.label)} disabled={busy}>
                  <span>{item.label}</span>
                  <small>{item.ref}</small>
                </button>
              )) : <p>No tags yet.</p>}
            </div>
          </div>
        </section>

        <section className="panel audit-panel">
          <div className="panel-head">
            <h2><BookOpenText size={16} /> Audit</h2>
            <div className="panel-actions">
              <StreamStatus value={auditStream} />
              <button disabled={!active || busy} onClick={() => reloadAudit()}>
                <RefreshCw size={16} /> Refresh
              </button>
              <button disabled={!active || busy} onClick={exportAuditCSV}>
                <Download size={16} /> CSV
              </button>
            </div>
          </div>
          <form className="audit-filters" onSubmit={applyAuditFilters}>
            <input
              type="datetime-local"
              aria-label="Audit from"
              value={auditFilters.from ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, from: event.target.value }))}
            />
            <input
              type="datetime-local"
              aria-label="Audit to"
              value={auditFilters.to ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, to: event.target.value }))}
            />
            <input
              placeholder="Actor"
              value={auditFilters.actor ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, actor: event.target.value }))}
            />
            <input
              placeholder="Action"
              value={auditFilters.action ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, action: event.target.value }))}
            />
            <select
              aria-label="Audit outcome"
              value={auditFilters.outcome ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, outcome: event.target.value }))}
            >
              <option value="">Any outcome</option>
              <option value="success">Success</option>
              <option value="failed">Failed</option>
              <option value="skipped">Skipped</option>
            </select>
            <select
              aria-label="Audit target engine"
              value={auditFilters.target_engine ?? ''}
              onChange={(event) => setAuditFilters((filters) => ({ ...filters, target_engine: event.target.value as AuditFilters['target_engine'] }))}
            >
              <option value="">Any engine</option>
              <option value="haproxy">HAProxy</option>
              <option value="nginx">Nginx</option>
            </select>
            <button disabled={!active || busy}>
              <RefreshCw size={15} /> Apply
            </button>
            <button type="button" disabled={!active || busy} onClick={clearAuditFilters}>
              Reset
            </button>
          </form>
          <AuditList events={audit} />
        </section>
      </section>
    </main>
  );
}

function DiffList({ changes }: { changes: DiffChange[] }) {
  if (!changes.length) {
    return null;
  }
  return (
    <div className="diff-list">
      {changes.map((change, index) => (
        <article key={`${change.kind}-${change.path}-${index}`} className={`diff-change ${change.kind}`}>
          <strong>{change.kind}</strong>
          <span>{change.entity_type} / {change.entity_id}</span>
          <code>{change.path}</code>
        </article>
      ))}
    </div>
  );
}

function NativeStatus({ native }: { native: NativeResult }) {
  if (native.skipped) {
    return (
      <div className="native-status skipped">
        <TriangleAlert size={15} />
        <span>{native.error || 'Native validation skipped.'}</span>
      </div>
    );
  }
  const ok = native.exit_code === 0;
  return (
    <div className={ok ? 'native-status ok-status' : 'native-status failed'}>
      {ok ? <CheckCircle2 size={15} /> : <TriangleAlert size={15} />}
      <span>{ok ? 'Native validation passed' : `Native validation failed with exit code ${native.exit_code}`}</span>
      {native.command && <code>{native.command}</code>}
      {(native.stderr || native.stdout) && <pre>{native.stderr || native.stdout}</pre>}
    </div>
  );
}

function DeployPlan({ result }: { result: DeployResult | null }) {
  if (!result) {
    return null;
  }
  return (
    <div className="deploy-plan">
      <div className="deploy-plan-head">
        <strong>{result.dry_run ? 'Dry-run deployment plan' : 'Deployment run'}</strong>
        <span>{result.status} / {result.steps.length} steps / {result.snapshot_hash.slice(0, 12)}</span>
      </div>
      <div className="deploy-steps">
        {result.steps.map((step, index) => (
          <article key={`${step.target_id}-${step.stage}-${index}`} className={`deploy-step ${step.status}`}>
            <div>
              <strong>{step.stage}</strong>
              <span>{step.target_name} / batch {step.batch}</span>
            </div>
            {step.command && <code>{step.command}</code>}
            {step.message && <small>{step.message}</small>}
          </article>
        ))}
      </div>
    </div>
  );
}

function ProbeStatus({ result }: { result: ProbeResult | null }) {
  if (!result) {
    return null;
  }
  return (
    <div className={`probe-status ${result.status}`}>
      <div>
        <strong>{result.target_name}</strong>
        <span>{result.status === 'success' ? 'Probe passed' : 'Probe failed'}</span>
      </div>
      <code>{result.url}</code>
      {result.message && <small>{result.message}</small>}
      <small>{new Date(result.checked_at).toLocaleString()}</small>
    </div>
  );
}

function StreamStatus({ value, label }: { value: 'idle' | 'connecting' | 'live' | 'error'; label?: string }) {
  const text = label ?? (value === 'live' ? 'Live' : value === 'connecting' ? 'Connecting' : value === 'error' ? 'Reconnecting' : 'Manual');
  return (
    <div className={`stream-status ${value}`}>
      <span aria-hidden="true" />
      <small>{text}</small>
    </div>
  );
}

function MonitorPanel({ snapshot, stream }: { snapshot: MonitorSnapshot | null; stream: 'idle' | 'connecting' | 'live' | 'error' }) {
  if (!snapshot) {
    return <div className="monitor-empty">No monitor snapshot loaded.</div>;
  }
  const streamLabel = stream === 'live' ? 'Live' : stream === 'connecting' ? 'Connecting' : stream === 'error' ? 'Reconnecting' : 'Manual';
  return (
    <div className="monitor-body">
      <StreamStatus value={stream} label={streamLabel} />
      <div className="monitor-summary">
        <Metric icon={<Server />} label="Targets" value={snapshot.summary.total_targets} />
        <Metric icon={<CheckCircle2 />} label="Healthy" value={snapshot.summary.healthy} />
        <Metric icon={<TriangleAlert />} label="Unknown" value={snapshot.summary.unknown} />
        <Metric icon={<Activity />} label="Failed" value={snapshot.summary.failed} />
      </div>
      <div className="monitor-list">
        {snapshot.targets.length ? snapshot.targets.map((target) => (
          <article key={target.target_id} className={`monitor-target ${target.status}`}>
            <div>
              <strong>{target.name}</strong>
              <span>{target.engine} / {target.host}</span>
            </div>
            <small>{target.message}</small>
          </article>
        )) : <p className="muted">No deployment targets to monitor yet.</p>}
      </div>
      <small className="monitor-updated">Updated {new Date(snapshot.generated_at).toLocaleString()}</small>
    </div>
  );
}

function ConfigPreview({ config }: { config: string }) {
  if (!config) {
    return <pre className="config-preview empty">Generated config will appear here.</pre>;
  }
  return (
    <div className="config-preview line-view">
      {config.split('\n').map((line, index) => (
        <div key={index} className="config-line">
          <span>{index + 1}</span>
          <code>{line || ' '}</code>
        </div>
      ))}
    </div>
  );
}

function Metric({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <div className="metric">
      {icon}
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function AuditList({ events }: { events: AuditEvent[] }) {
  if (!events.length) {
    return <div className="audit-empty">No audit events yet.</div>;
  }
  return (
    <div className="audit-list">
      {events.slice(0, 12).map((event) => (
        <article key={event.event_id} className={`audit-event ${event.outcome}`}>
          <div>
            <strong>{event.action}</strong>
            <span>{new Date(event.timestamp).toLocaleString()}</span>
          </div>
          <small>{event.actor}{event.target_engine ? ` / ${event.target_engine}` : ''}{event.ir_snapshot_hash ? ` / ${event.ir_snapshot_hash.slice(0, 12)}` : ''}</small>
          {event.error_message && <p>{event.error_message}</p>}
        </article>
      ))}
    </div>
  );
}

function auditMatchesFilters(event: AuditEvent, filters: AuditFilters) {
  const timestamp = new Date(event.timestamp).getTime();
  if (filters.from && timestamp < new Date(filters.from).getTime()) {
    return false;
  }
  if (filters.to && timestamp > new Date(filters.to).getTime()) {
    return false;
  }
  if (filters.actor && event.actor.toLowerCase() !== filters.actor.toLowerCase()) {
    return false;
  }
  if (filters.action && event.action !== filters.action) {
    return false;
  }
  if (filters.outcome && event.outcome !== filters.outcome) {
    return false;
  }
  if (filters.target_engine && event.target_engine !== filters.target_engine) {
    return false;
  }
  return true;
}

function IssueList({ issues }: { issues: IRResponse['issues'] }) {
  if (!issues.length) {
    return <div className="ok"><CheckCircle2 size={16} /> No structural issues.</div>;
  }
  return (
    <div className="issues">
      {issues.map((issue, index) => (
        <div key={index} className={issue.severity}>
          <TriangleAlert size={15} />
          <span>{issue.entity_id ? `${issue.entity_id}: ` : ''}{issue.message}</span>
        </div>
      ))}
    </div>
  );
}

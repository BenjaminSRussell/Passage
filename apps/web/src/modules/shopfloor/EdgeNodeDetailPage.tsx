import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  DUTY_EDGE_CONFIGURE,
  getEdgeNode,
  listEdgeConfigs,
  mintEdgeEnrollmentToken,
  newKey,
  publishEdgeConfig,
  updateEdgeNode,
  type EdgeConfigVersion,
  type EdgeEnrollmentToken,
  type EdgeNode,
  type EdgeRing,
  type Problem,
} from "./api/client";
import { configState, EdgeStatusBadge, RING_LABELS } from "./EdgeNodesPage";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Parses the document text; the service validates the content. */
export function parseDocument(text: string): { document?: Record<string, unknown>; error?: string } {
  try {
    const value: unknown = JSON.parse(text);
    if (value === null || typeof value !== "object" || Array.isArray(value)) return { error: "The document must be a JSON object." };
    return { document: value as Record<string, unknown> };
  } catch {
    return { error: "Enter valid JSON." };
  }
}

const versionColumns: DataTableColumn<EdgeConfigVersion>[] = [
  { key: "version", header: "Version", align: "end", mono: true },
  { key: "publishedAt", header: "Published", mono: true, render: (row) => formatDateTime(row.publishedAt) },
  { key: "publishedBy", header: "By" },
  { key: "appliedAt", header: "First applied", mono: true, render: (row) => (row.appliedAt ? formatDateTime(row.appliedAt) : "Not yet") },
  { key: "documentSha256", header: "SHA-256", mono: true, render: (row) => <span title={row.documentSha256}>{row.documentSha256.slice(0, 12)}</span> },
  {
    key: "document",
    header: "Document",
    render: (row) => (
      <details>
        <summary>{`View version ${row.version}`}</summary>
        <pre className={styles.file} tabIndex={0} aria-label={`Configuration version ${row.version}`}>
          {JSON.stringify(row.document, null, 2)}
        </pre>
      </details>
    ),
  },
];

/** One edge node: its heartbeat, machines, ring, and configuration versions with a publish form (FR-25.19). */
export function EdgeNodeDetailPage() {
  const { id = "" } = useParams();
  const canConfigure = holdsDuty(usePrincipalDuties(), DUTY_EDGE_CONFIGURE);
  const node = useLoad((signal) => getEdgeNode(id, signal), id);
  const configs = useLoad((signal) => listEdgeConfigs(id, signal), `${id}:configs`);
  const [fresh, setFresh] = useState<EdgeNode | null>(null);
  const current = fresh && fresh.id === id ? fresh : node.data;

  return (
    <div className={styles.stack}>
      <PageHeader title={current?.name ?? "Edge node"} description={<Link to="/shopfloor/edge-nodes">All edge nodes</Link>} />
      {node.error && !current ? (
        <Alert tone="danger" title="Could not load the edge node" live="alert" action={<Button size="sm" onClick={node.reload}>Try again</Button>}>
          <ProblemMessage problem={node.error} />
        </Alert>
      ) : !current ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          <Card>
            <dl className={styles.details}>
              <Entry term="Status" value={<EdgeStatusBadge status={current.status} />} />
              <Entry term="Platform" value={current.platform} />
              <Entry term="Ring" value={RING_LABELS[current.ring]} />
              <Entry term="Connector identity" value={current.connectorSubject ?? "Not bound: pulls and heartbeats are refused"} />
              <Entry term="Connector version" value={current.connectorVersion ?? dash} />
              <Entry term="Last heartbeat" value={formatDateTime(current.lastHeartbeatAt)} />
              <Entry term="Buffer fill" value={current.bufferFillPct === null ? dash : `${current.bufferFillPct}%`} />
              <Entry term="Configuration" value={configState(current)} />
              <Entry term="Certificate expires" value={formatDateTime(current.certExpiresAt)} />
            </dl>
          </Card>
          <Card title="Machines">
            {current.machines.length === 0 ? (
              <p className={styles.note}>No machine states reported yet.</p>
            ) : (
              <ul>
                {current.machines.map((m) => (
                  <li key={m.machineCode}>{`${m.machineCode}: ${m.state}`}</li>
                ))}
              </ul>
            )}
          </Card>
          {canConfigure ? (
            <>
              <EditForm node={current} onSaved={setFresh} />
              <EnrollmentCard node={current} />
              <PublishForm
                nodeId={id}
                onPublished={() => {
                  configs.reload();
                  node.reload();
                  setFresh(null);
                }}
              />
            </>
          ) : (
            <p className={styles.note}>Changing the node and publishing its configuration needs the edge fleet duty ({DUTY_EDGE_CONFIGURE}).</p>
          )}
          {configs.error ? (
            <Alert tone="danger" title="Could not load configuration versions" live="alert" action={<Button size="sm" onClick={configs.reload}>Try again</Button>}>
              <ProblemMessage problem={configs.error} />
            </Alert>
          ) : (
            <DataTable
              label="Configuration versions"
              columns={versionColumns}
              rows={configs.data ?? []}
              getRowKey={(row) => row.id}
              loading={configs.loading}
              empty={{ title: "No configuration published", description: "Publish a document below; the connector pulls the newest version." }}
            />
          )}
        </>
      )}
    </div>
  );
}

function Entry({ term, value }: { term: string; value: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{value}</dd>
    </div>
  );
}

function EditForm({ node, onSaved }: { node: EdgeNode; onSaved(saved: EdgeNode): void }) {
  const [ring, setRing] = useState<EdgeRing>(node.ring);
  const [subject, setSubject] = useState(node.connectorSubject ?? "");
  const [active, setActive] = useState(node.active ? "on" : "off");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    setBusy(true);
    try {
      const connectorSubject = subject.trim() === "" ? null : subject.trim();
      onSaved(await updateEdgeNode(node.id, node.version, { name: node.name, ring, siteId: node.siteId, connectorSubject, active: active === "on" }));
      setSaved(true);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Ring, connector identity and status">
      <form className={styles.form} onSubmit={submit}>
        <FormField id="edge-ring-edit" label="Rollout ring">
          {(control) => (
            <Select {...control} value={ring} onChange={(event) => setRing(event.target.value as EdgeRing)}>
              <option value="canary">Canary</option>
              <option value="ring1">Ring 1</option>
              <option value="all">All</option>
            </Select>
          )}
        </FormField>
        <FormField
          id="edge-subject-edit"
          label="Connector identity"
          optional
          hint="Only this identity may pull the configuration and report heartbeats. Clear it to unbind the node."
        >
          {(control) => <Input {...control} maxLength={200} value={subject} onChange={(event) => setSubject(event.target.value)} />}
        </FormField>
        <FormField id="edge-active-edit" label="Connector" hint="A switched-off node refuses heartbeats and configuration pulls.">
          {(control) => (
            <Select {...control} value={active} onChange={(event) => setActive(event.target.value)}>
              <option value="on">Switched on</option>
              <option value="off">Switched off</option>
            </Select>
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the edge node" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Saved" live="status">
              The ring, connector identity and status were saved.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Save node
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Mints the one-time token a connector redeems for its certificate (FR-25.19). The token is shown once. */
function EnrollmentCard({ node }: { node: EdgeNode }) {
  const [token, setToken] = useState<EdgeEnrollmentToken | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function mint() {
    setFailure(null);
    setBusy(true);
    try {
      setToken(await mintEdgeEnrollmentToken(node.id));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Enroll the connector">
      <div className={styles.stack}>
        <p className={styles.note}>
          Give the connector a one-time token. It redeems the token for a 90-day client certificate and from then on is the only identity that may pull this node&apos;s configuration.
          A new token replaces any earlier one that was not used.
        </p>
        {!node.active ? <p className={styles.note}>This node is switched off. Switch it on to enroll its connector.</p> : null}
        {failure ? (
          <Alert tone="danger" title="Could not create an enrollment token" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        {token ? (
          <Alert tone="success" title="Enrollment token" live="status">
            {`Copy it now; it is not shown again. It expires ${formatDateTime(token.expiresAt)}.`}
            <pre className={styles.file} tabIndex={0} aria-label="Enrollment token">
              {token.token}
            </pre>
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="button" loading={busy} disabled={!node.active} onClick={mint}>
            Create enrollment token
          </Button>
        </div>
      </div>
    </Card>
  );
}

function PublishForm({ nodeId, onPublished }: { nodeId: string; onPublished(): void }) {
  const [text, setText] = useState('{\n  "machines": [],\n  "tags": []\n}');
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [published, setPublished] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setPublished(null);
    const { document, error: parseError } = parseDocument(text);
    setError(parseError ?? null);
    if (!document) return;
    setBusy(true);
    try {
      const version = await publishEdgeConfig(nodeId, document, key.current);
      key.current = newKey();
      setPublished(version.version);
      onPublished();
    } catch (err) {
      const problem = describeProblem(err);
      const field = Object.values(problem.fields)[0];
      if (field) setError(field);
      else setFailure(problem);
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Publish a configuration">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="edge-document" label="Configuration document (JSON)" hint="machines, tags and writeRules; the server numbers the version." error={error ?? undefined}>
          {(control) => <Textarea {...control} rows={10} value={text} onChange={(event) => { setText(event.target.value); key.current = newKey(); }} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not publish the configuration" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {published !== null ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Published" live="status">{`Version ${published} is published; the connector picks it up on its next pull.`}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Publish configuration
          </Button>
        </div>
      </form>
    </Card>
  );
}

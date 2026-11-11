import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { ApiError, createEco, getEco, listEcos, releaseEco, type Eco, type EcoReplacement } from "./api/client";
import { EcoImpactPanel } from "./impact";
import styles from "./pages.module.css";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const ecoStates = ["draft", "submitted", "approved", "scheduled", "released", "rejected", "cancelled"];
const releasable = ["submitted", "approved", "scheduled"];
const notFoundCode = "ENG-ECO-NOTFOUND";
const forbiddenStatus = 403;

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

const ecoColumns: DataTableColumn<Eco>[] = [
  { key: "number", header: "ECO", mono: true },
  { key: "state", header: "State", render: (row) => <StatusPill status={row.state} /> },
  { key: "releaseDate", header: "Release date", render: (row) => row.releaseDate ?? "On release" },
  { key: "replaces", header: "Replacements", align: "end", render: (row) => row.replaces.length },
  { key: "decidedBy", header: "Decided by", render: (row) => row.decidedBy || "Pending" },
];

export function EcosPage({ onOpen, onCreate }: { onOpen?: (id: string) => void; onCreate?: () => void }) {
  const [state, setState] = useState("");
  const [rows, setRows] = useState<Eco[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    let current = true;
    generation.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError("");
    listEcos(state)
      .then((page) => {
        if (!current) return;
        setRows(page.items);
        setNextCursor(page.nextCursor ?? "");
      })
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setNextCursor("");
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [state, attempt]);

  async function loadMore() {
    const token = generation.current;
    setLoadingMore(true);
    setError("");
    try {
      const page = await listEcos(state, nextCursor);
      if (token !== generation.current) return;
      setRows((existing) => [...existing, ...page.items]);
      setNextCursor(page.nextCursor ?? "");
    } catch (e) {
      if (token === generation.current) setError(errorText(e));
    } finally {
      if (token === generation.current) setLoadingMore(false);
    }
  }

  return (
    <>
      <PageHeader title="Engineering change orders" description="Controlled replacement of active BOM versions." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New ECO
            </Button>
          ) : undefined
        }
      >
        <div className={styles.form}>
          <div className={styles.field}>
            <FormField id="eco-state" label="State" optional>
              {(control) => (
                <Select {...control} value={state} onChange={(e) => setState(e.target.value)}>
                  <option value="">Any state</option>
                  {ecoStates.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </div>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Engineering change orders"
            columns={ecoColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No engineering change orders match" }}
            onRowOpen={(row) => onOpen?.(row.id)}
          />
        )}
      </div>
      {nextCursor !== "" && !loading ? (
        <div className={styles.toolbar}>
          <Button loading={loadingMore} onClick={loadMore}>Load more</Button>
        </div>
      ) : null}
    </>
  );
}

export function EcoPage({ id, onBack, onOpenBom, onOpenEcr }: { id: string; onBack?: () => void; onOpenBom?: (versionId: string) => void; onOpenEcr?: (ecrId: string) => void }) {
  const [eco, setEco] = useState<Eco | null>(null);
  const [loadError, setLoadError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [releasing, setReleasing] = useState(false);
  const [releaseError, setReleaseError] = useState("");
  const [announcement, setAnnouncement] = useState("");
  // One key per ECO until a release succeeds; the service also answers a repeat release of a released ECO unchanged.
  const releaseKey = useRef({ id: "", key: "" });

  useEffect(() => {
    let current = true;
    setEco(null);
    setLoadError(null);
    setReleaseError("");
    setAnnouncement("");
    getEco(id)
      .then((loaded) => current && setEco(loaded))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  async function release() {
    setReleaseError("");
    setAnnouncement("");
    setReleasing(true);
    if (releaseKey.current.id !== id) releaseKey.current = { id, key: crypto.randomUUID() };
    try {
      const updated = await releaseEco(id, releaseKey.current.key);
      releaseKey.current = { id: "", key: "" };
      setEco(updated);
      setAnnouncement(`ECO ${updated.number} is ${updated.state}`);
    } catch (e) {
      const forbidden = e instanceof ApiError && e.status === forbiddenStatus;
      setReleaseError(forbidden ? "You do not have permission to release ECOs" : errorText(e));
      // A refused release can still change the ECO (an approver's rejection is persisted), so show its current state.
      if (!forbidden) {
        await getEco(id)
          .then((current) => {
            if (current.id === id) setEco(current);
          })
          .catch(() => undefined);
      }
    } finally {
      setReleasing(false);
    }
  }

  const notFound = loadError instanceof ApiError && loadError.code === notFoundCode;
  const alertText = loadError ? (notFound ? "This ECO does not exist" : errorText(loadError)) : releaseError;
  const facts: [string, React.ReactNode][] = eco
    ? [
        ["Number", eco.number],
        ["State", <StatusPill status={eco.state} />],
        ["Release date", eco.releaseDate ?? "On release"],
        ["Decided by", eco.decidedBy || "Pending"],
        ...(eco.ecrId ? [["Change request", onOpenEcr ? <Button size="sm" onClick={() => onOpenEcr(eco.ecrId!)}>Open change request {eco.ecrId}</Button> : eco.ecrId] as [string, React.ReactNode]] : []),
      ]
    : [];
  const columns: DataTableColumn<EcoReplacement>[] = [
    {
      key: "oldVersionId",
      header: "Expires (old version)",
      mono: true,
      render: (row) => (onOpenBom ? <Button size="sm" onClick={() => onOpenBom(row.oldVersionId)}>Open old {row.oldVersionId}</Button> : row.oldVersionId),
    },
    {
      key: "newVersionId",
      header: "Activates (new version)",
      mono: true,
      render: (row) => (onOpenBom ? <Button size="sm" onClick={() => onOpenBom(row.newVersionId)}>Open new {row.newVersionId}</Button> : row.newVersionId),
    },
  ];

  return (
    <>
      <PageHeader title={eco ? `ECO ${eco.number}` : "Engineering change order"} description="State, release date and the BOM versions replaced." />
      <div className={styles.toolbar}>
        {onBack ? <Button size="sm" onClick={onBack}>Back to change orders</Button> : null}
        {loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : null}
        {eco && releasable.includes(eco.state) ? (
          <Button size="sm" variant="primary" loading={releasing} onClick={release}>
            Approve and release
          </Button>
        ) : null}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {announcement}
      </p>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {alertText || null}
        </Alert>
      </div>
      {!eco && !loadError ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {eco ? (
        <>
          <Card title="Change order">
            <dl className={styles.facts}>
              {facts.map(([term, value]) => (
                <Fragment key={term}>
                  <dt>{term}</dt>
                  <dd>{value}</dd>
                </Fragment>
              ))}
            </dl>
          </Card>
          <div className={styles.results}>
            <DataTable
              label="Replacements"
              columns={columns}
              rows={eco.replaces}
              getRowKey={(row) => row.newVersionId}
              empty={{ title: "No replacements" }}
            />
          </div>
          <EcoImpactPanel key={id} id={id} />
        </>
      ) : null}
    </>
  );
}

interface PairDraft {
  key: number;
  oldVersionId: string;
  newVersionId: string;
}

// BR-9.10 refusals of the release date (past without the backdate duty, or beyond the horizon).
const releaseDateCodes = new Set(["ENG-ECO-HORIZON", "ENG-ECO-BACKDATE-DUTY"]);

export function EcoCreatePage({ onCreated, onCancel, initialEcrId = "" }: { onCreated?: (id: string) => void; onCancel?: () => void; initialEcrId?: string }) {
  const [number, setNumber] = useState("");
  const [ecrId, setEcrId] = useState(initialEcrId);
  const [releaseDate, setReleaseDate] = useState("");
  const [pairs, setPairs] = useState<PairDraft[]>([{ key: 0, oldVersionId: "", newVersionId: "" }]);
  const nextKey = useRef(1);
  const idempotency = useRef({ body: "", key: "" });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});

  function updatePair(key: number, patch: Partial<PairDraft>) {
    setPairs((existing) => existing.map((p) => (p.key === key ? { ...p, ...patch } : p)));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFields({});
    setSubmitting(true);
    const input = {
      number: number.trim(),
      ...(releaseDate ? { releaseDate } : {}),
      ...(ecrId.trim() ? { ecrId: ecrId.trim() } : {}),
      replaces: pairs.map((p) => ({ oldVersionId: p.oldVersionId.trim(), newVersionId: p.newVersionId.trim() })),
    };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createEco(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      if (e instanceof ApiError && releaseDateCodes.has(e.code ?? "")) {
        setError(errorText(e));
        setFields({ releaseDate: e.message });
      } else if (e instanceof ApiError && e.status === forbiddenStatus) setError("You do not have permission to create ECOs");
      else {
        setError(errorText(e));
        if (e instanceof ApiError) setFields(e.errors);
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New engineering change order" description="Replace active BOM versions with draft versions of the same product." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-eco-number" label="ECO number" error={fields.number}>
              {(control) => <Input {...control} value={number} onChange={(e) => setNumber(e.target.value)} required maxLength={40} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-eco-date" label="Release date" optional error={fields.releaseDate}>
              {(control) => <Input {...control} type="date" value={releaseDate} onChange={(e) => setReleaseDate(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <LookupFormField id="new-eco-ecr" label="Change request" entity="ecr" optional hint="An approved ECR that lists every product this ECO replaces." value={ecrId} error={fields.ecrId} onChange={setEcrId} />
          </div>
          {pairs.map((pair, index) => (
            <Fragment key={pair.key}>
              <div className={styles.field}>
                <FormField id={`new-eco-old-${pair.key}`} label={`Old version ${index + 1}`} error={index === 0 ? fields.replaces : undefined}>
                  {(control) => <Input {...control} value={pair.oldVersionId} onChange={(e) => updatePair(pair.key, { oldVersionId: e.target.value })} required />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-eco-new-${pair.key}`} label={`New version ${index + 1}`}>
                  {(control) => <Input {...control} value={pair.newVersionId} onChange={(e) => updatePair(pair.key, { newVersionId: e.target.value })} required />}
                </FormField>
              </div>
              {pairs.length > 1 ? (
                <Button size="sm" onClick={() => setPairs((existing) => existing.filter((p) => p.key !== pair.key))}>
                  Remove replacement {index + 1}
                </Button>
              ) : null}
            </Fragment>
          ))}
          <div className={styles.toolbar}>
            <Button onClick={() => setPairs((existing) => [...existing, { key: nextKey.current++, oldVersionId: "", newVersionId: "" }])}>Add replacement</Button>
            <Button type="submit" variant="primary" loading={submitting}>Create ECO</Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
  );
}

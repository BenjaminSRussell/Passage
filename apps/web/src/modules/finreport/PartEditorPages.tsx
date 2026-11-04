import { useEffect, useState, type FormEvent, type ReactElement } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import {
  describeProblem,
  getColumnSet,
  getRowSet,
  listColumnTemplates,
  saveColumnSet,
  saveRowSet,
  type ColumnSet,
  type DefinitionColumn,
  type DefinitionRow,
  type Problem,
  type RowSet,
} from "./api/client";
import { DEFINITION_CODE } from "./format";
import { ColumnsEditor, RowsEditor } from "./PartEditors";
import { cleanColumn, cleanRow } from "./DefinitionEditorPage";
import styles from "./pages.module.css";

/** What differs between the row set and the column set editor. */
interface PartKind<T extends { code: string; name: string }> {
  noun: string;
  listPath: string;
  load(code: string, signal: AbortSignal): Promise<{ part: T; etag: string | null }>;
  save(part: T, etag: string | null): Promise<{ part: T; etag: string | null }>;
  blank(): T;
  clean(part: T): T;
  /** A new part may start from something else, such as a template named in the query string. */
  start?(search: URLSearchParams, signal: AbortSignal): Promise<T | null>;
  body(part: T, change: (patch: Partial<T>) => void): ReactElement;
  description: string;
}

export function blankRowSet(): RowSet {
  return { code: "", name: "", rows: [{ code: "R1", kind: "account_range", label: "", accountFrom: "", accountTo: "", normalBalance: "debit", format: {} }] };
}

export function blankColumnSet(): ColumnSet {
  return { code: "", name: "", description: "", columns: [{ code: "C1", label: "Period", periodExpr: "P" }] };
}

const rowSetKind: PartKind<RowSet> = {
  noun: "row set",
  listPath: "/finreport/row-sets",
  load: (code, signal) => getRowSet(code, signal),
  save: (part, etag) => saveRowSet(part, etag),
  blank: blankRowSet,
  clean: (s) => ({ code: s.code.trim(), name: s.name, rows: s.rows.map((r: DefinitionRow) => cleanRow(r)) }),
  body: (part, change) => <RowsEditor rows={part.rows} onChange={(rows) => change({ rows })} />,
  description: "A named list of statement rows. Report definitions can start from it.",
};

const columnSetKind: PartKind<ColumnSet> = {
  noun: "column set",
  listPath: "/finreport/column-sets",
  load: (code, signal) => getColumnSet(code, signal),
  save: (part, etag) => saveColumnSet(part, etag),
  blank: blankColumnSet,
  clean: (s) => ({ code: s.code.trim(), name: s.name, description: s.description || undefined, columns: s.columns.map((c: DefinitionColumn) => cleanColumn(c)) }),
  start: async (search, signal) => {
    const code = search.get("template");
    if (!code) return null;
    const found = (await listColumnTemplates(signal)).find((t) => t.code === code);
    if (!found) throw new Error(`There is no template ${code}.`);
    return { ...found, code: "", name: found.name };
  },
  body: (part, change) => <ColumnsEditor columns={part.columns} onChange={(columns) => change({ columns })} />,
  description: "A named list of statement columns, with period expressions and variance calculations.",
};

function PartEditor<T extends { code: string; name: string; description?: string }>({ kind }: { kind: PartKind<T> }) {
  const { code: routeCode } = useParams();
  const [search] = useSearchParams();
  const isNew = routeCode === undefined;
  const navigate = useNavigate();
  const [part, setPart] = useState<T | null>(null);
  const [etag, setEtag] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<Problem | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [codeError, setCodeError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const template = search.get("template");

  useEffect(() => {
    const controller = new AbortController();
    setPart(null);
    setLoadError(null);
    const done = (next: T, tag: string | null) => {
      setPart(next);
      setEtag(tag);
      setDirty(false);
    };
    const fail = (error: unknown) => {
      if (!controller.signal.aborted) setLoadError(describeProblem(error));
    };
    if (isNew) {
      (kind.start ? kind.start(new URLSearchParams(search), controller.signal) : Promise.resolve(null)).then((started) => {
        done(started ?? kind.blank(), null);
        if (started) setDirty(true);
      }, fail);
    } else {
      kind.load(routeCode ?? "", controller.signal).then((res) => done(res.part, res.etag), fail);
    }
    return () => controller.abort();
    // The kind is a module constant.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeCode, isNew, template, attempt]);

  const title = isNew ? `New ${kind.noun}` : `${kind.noun[0].toUpperCase()}${kind.noun.slice(1)} ${routeCode}`;
  if (loadError) {
    const missing = loadError.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? `This ${kind.noun} does not exist` : `This ${kind.noun} could not be loaded`}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Try again</Button>}
        >
          {loadError.message} ({loadError.code})
        </Alert>
        <Link to={kind.listPath}>Back to the {kind.noun}s</Link>
      </div>
    );
  }
  if (!part) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading {kind.noun}…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }
  const current = part;

  function change(patch: Partial<T>) {
    setPart((prev) => (prev ? { ...prev, ...patch } : prev));
    setDirty(true);
    setNotice(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!DEFINITION_CODE.test(current.code.trim())) {
      setCodeError("Use 2 to 30 upper-case letters, digits or hyphens, starting with a letter.");
      return;
    }
    setCodeError(null);
    setBusy(true);
    setFailure(null);
    try {
      const res = await kind.save(kind.clean(current), etag);
      setPart(res.part);
      setEtag(res.etag);
      setDirty(false);
      setNotice(`The ${kind.noun} was saved.`);
      if (isNew) navigate(`${kind.listPath}/${encodeURIComponent(res.part.code)}`, { replace: true });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "conflict_version" || failure?.code === "precondition_required";
  return (
    <div className={styles.stack}>
      <PageHeader title={title} description={kind.description} meta={!isNew ? <Badge tone="info">{`Revision ${etag?.replace(/"/g, "") ?? "?"}`}</Badge> : undefined} />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title={kind.noun[0].toUpperCase() + kind.noun.slice(1)}>
          <div className={styles.form}>
            <FormField id="fr-part-code" label={`${kind.noun[0].toUpperCase()}${kind.noun.slice(1)} code`} hint={isNew ? "Upper-case letters, digits and hyphens, such as CMP-PY." : undefined} error={codeError ?? undefined}>
              {(c) => <Input {...c} className={styles.mono} value={current.code} readOnly={!isNew} invalid={Boolean(codeError)} onChange={(e) => change({ code: e.target.value.toUpperCase() } as Partial<T>)} />}
            </FormField>
            <FormField id="fr-part-name" label="Name">
              {(c) => <Input {...c} value={current.name} onChange={(e) => change({ name: e.target.value } as Partial<T>)} />}
            </FormField>
            {"description" in current ? (
              <FormField id="fr-part-description" label="Description" optional>
                {(c) => <Input {...c} value={current.description ?? ""} onChange={(e) => change({ description: e.target.value } as Partial<T>)} />}
              </FormField>
            ) : null}
          </div>
        </Card>
        {kind.body(current, change)}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            {`Save ${kind.noun}`}
          </Button>
          <Link to={kind.listPath}>Back to the {kind.noun}s</Link>
          {dirty ? <span className={styles.note}>Unsaved changes.</span> : null}
        </div>
      </form>
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? `Someone else changed this ${kind.noun}` : `The ${kind.noun} was not saved`}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
                {`Reload the ${kind.noun}`}
              </Button>
            ) : undefined
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </div>
  );
}

export function RowSetEditorPage() {
  return <PartEditor kind={rowSetKind} />;
}

export function ColumnSetEditorPage() {
  return <PartEditor kind={columnSetKind} />;
}

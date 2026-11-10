import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  approveBatchRecord,
  batchRecordPdfUrl,
  compileBatchRecord,
  defineRecordProfile,
  describeProblem,
  dispositionRecordException,
  getBatchRecord,
  listBatchRecords,
  listRecordProfiles,
  newKey,
  raiseRecordException,
  retireRecordProfile,
  sealBatchRecord,
  type BatchRecord,
  type RecordDisposition,
  type ExceptionSeverity,
  type Problem,
  type RecordException,
  type RecordProfile,
  type RecordStatus,
} from "./api/client";
import { dash, formatDateTime, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Review duty: compiles records, raises and decides exceptions, maintains profiles; enforced by the service (FR-24.21, PRD 4.24.17). */
export const RECORD_WRITE_DUTY = "production.batch-record.write";
/** QA duty that approves and seals (BR-24.15); the service refuses an approver who reviewed the same record (PMF-0124). */
export const RECORD_APPROVE_DUTY = "production.batch-record.approve";

/** Shown wherever a decision or an approval is signed (21 CFR Part 11). */
const SIGNING_NOTE =
  "Recording this signs it electronically under your name: the service asks you to re-authenticate and binds the signature to what you see now.";

const STATUS_LABEL: Record<RecordStatus, string> = { "in-review": "In review", approved: "Approved", sealed: "Sealed" };
const STATUS_TONE: Record<RecordStatus, "warning" | "info" | "success"> = { "in-review": "warning", approved: "info", sealed: "success" };
const DISPOSITION_LABEL: Record<RecordDisposition, string> = { accepted: "Accepted", "deviation-raised": "Deviation raised", rejected: "Rejected" };

function RecordStatusBadge({ status }: { status: RecordStatus }) {
  return <Badge tone={STATUS_TONE[status]}>{STATUS_LABEL[status]}</Badge>;
}

function LoadError({ title, error, reload }: { title: string; error: Problem; reload: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      <ProblemMessage problem={error} />
    </Alert>
  );
}

function Missing({ title, children }: { title: string; children: string }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Card title={title}>
        <p className={styles.note}>{children}</p>
      </Card>
    </div>
  );
}

const recordColumns: DataTableColumn<BatchRecord>[] = [
  { key: "batchNumber", header: "Batch", mono: true, render: (r) => r.batchNumber },
  { key: "itemId", header: "Item", mono: true, render: (r) => shortId(r.itemId) },
  { key: "status", header: "Status", render: (r) => <RecordStatusBadge status={r.status} /> },
  { key: "exceptionCount", header: "Exceptions", align: "end", render: (r) => String(r.exceptionCount) },
  { key: "openExceptions", header: "Open", align: "end", render: (r) => String(r.openExceptions) },
  { key: "compiledAt", header: "Compiled", render: (r) => formatDateTime(r.compiledAt) },
];

/** FR-24.21: the electronic batch records, with a form to compile the record of a batch. */
export function BatchRecordsPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), RECORD_WRITE_DUTY);
  const [status, setStatus] = useState("");
  const [batchId, setBatchId] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const state = useLoad((signal) => listBatchRecords({ status }, signal), `records|${status}`);

  async function compile(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!UUID.test(batchId.trim())) {
      setError("Enter the batch id as a UUID.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const record = await compileBatchRecord(batchId.trim(), key.current);
      key.current = newKey();
      navigate(`/processmfg/batch-records/${record.id}`);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch records"
        description="The electronic batch record of each batch: compiled from its batch order, reviewed by exception, approved and sealed. Releasing a batch whose item has a profile that asks for it needs the approved record."
        actions={
          <Link className={buttonClassName("ghost")} to="/processmfg/batch-record-profiles">
            Record profiles
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rec-filter-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All</option>
              <option value="in-review">In review</option>
              <option value="approved">Approved</option>
              <option value="sealed">Sealed</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the batch records" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Batch records"
          columns={recordColumns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No records match", description: "Choose another status to see more." }
              : { title: "No batch records yet", description: "Compile the record of a batch that has a batch order and an item profile." }
          }
          onRowOpen={(r) => navigate(`/processmfg/batch-records/${r.id}`)}
        />
      )}
      {canWrite ? (
        <Card title="Compile a record">
          <form className={styles.form} onSubmit={compile} noValidate>
            <FormField id="rec-compile-batch" label="Batch id" hint="A registered batch linked to a batch order." error={error}>
              {(c) => <Input {...c} value={batchId} onChange={(e) => { setBatchId(e.target.value); key.current = newKey(); }} invalid={Boolean(error)} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not compile the record" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Compile record
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}

type Action = "compile" | "raise" | "decide" | "approve" | "seal";

/** One record: its exceptions, the review steps for a holder of the duties, and the seal. */
export function BatchRecordDetailPage() {
  const { id = "" } = useParams();
  const duties = usePrincipalDuties();
  const canWrite = holdsDuty(duties, RECORD_WRITE_DUTY);
  const canApprove = holdsDuty(duties, RECORD_APPROVE_DUTY);
  const state = useLoad((signal) => getBatchRecord(id, signal), id);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<Action | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [disposition, setDisposition] = useState<RecordDisposition>("accepted");
  const [comment, setComment] = useState("");
  const [printedName, setPrintedName] = useState("");
  const [text, setText] = useState("");
  const [severity, setSeverity] = useState<ExceptionSeverity>("major");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const keys = useRef<Record<string, string>>({});

  function key(action: Action): string {
    keys.current[action] ??= newKey();
    return keys.current[action];
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Batch record" />
        <LoadError title="Could not load the batch record" error={state.error} reload={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Batch record" />
        <span className="visually-hidden">Loading the batch record…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const record = state.data;
  const exceptions = record.exceptions ?? [];
  const inReview = record.status === "in-review";

  async function run(action: Action, call: (k: string) => Promise<unknown>, after?: () => void) {
    setBusy(action);
    setFailure(null);
    try {
      await call(key(action));
      delete keys.current[action];
      after?.();
      state.reload();
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      // A refused command can be corrected and retried under a new key; an answer lost in transit is retried under the same one.
      if (problem.code !== "COM-0500") delete keys.current[action];
    } finally {
      setBusy(null);
    }
  }

  function decide(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (printedName.trim() === "" || printedName.trim().length > 200) found.printedName = "Enter your printed name to sign.";
    if (comment.trim().length > 500) found.comment = "At most 500 characters.";
    setErrors(found);
    if (Object.keys(found).length > 0 || !selected) return;
    void run(
      "decide",
      (k) => dispositionRecordException(record.id, selected, { version: record.version, disposition, comment: comment.trim() || undefined, printedName: printedName.trim() }, k),
      () => {
        setSelected(null);
        setComment("");
      },
    );
  }

  function raise(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (text.trim() === "" || text.trim().length > 500) found.text = "Describe the exception in 1 to 500 characters.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void run("raise", (k) => raiseRecordException(record.id, { version: record.version, description: text.trim(), severity }, k), () => setText(""));
  }

  const columns: DataTableColumn<RecordException>[] = [
    { key: "description", header: "Exception", render: (e) => e.description },
    { key: "severity", header: "Severity", render: (e) => e.severity },
    { key: "sourceType", header: "Source", render: (e) => (e.sourceType === "attribute" ? "Recorded value" : "Reviewer") },
    {
      key: "disposition",
      header: "Decision",
      render: (e) => (e.disposition ? <Badge tone={e.disposition === "rejected" ? "danger" : "success"}>{DISPOSITION_LABEL[e.disposition]}</Badge> : <Badge tone="warning">Open</Badge>),
    },
    { key: "signatureId", header: "Signature", mono: true, render: (e) => (e.signatureId ? shortId(e.signatureId) : dash) },
    { key: "dispositionedBy", header: "Decided by", render: (e) => e.dispositionedBy ?? dash },
    {
      key: "id",
      header: "Action",
      render: (e) =>
        canWrite && inReview && !e.disposition ? (
          <Button size="sm" onClick={() => { setSelected(e.id); setErrors({}); }}>
            Decide
          </Button>
        ) : (
          dash
        ),
    },
  ];
  const chosen = exceptions.find((e) => e.id === selected);

  return (
    <div className={styles.stack}>
      <PageHeader title={`Batch record of ${record.batchNumber}`} description={`Item ${record.itemId}, batch order ${record.batchOrderId}`} meta={<RecordStatusBadge status={record.status} />} />
      <StatGroup label="Batch record">
        <StatTile label="Exceptions" value={String(record.exceptionCount)} />
        <StatTile label="Open" value={String(record.openExceptions)} />
        <StatTile label="Compiled" value={formatDateTime(record.compiledAt)} />
        <StatTile label="Approved" value={record.approvedAt ? `${record.approvedBy ?? ""} ${formatDateTime(record.approvedAt)}`.trim() : dash} />
        <StatTile label="Sealed" value={record.sealedAt ? formatDateTime(record.sealedAt) : dash} />
      </StatGroup>
      {failure ? (
        <Alert tone="danger" title="The step was refused" live="alert" action={failure.code === "PMF-0104" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}>
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {record.status === "sealed" ? (
        <Card title="Sealed">
          <p className={styles.note}>
            Sealed by {record.sealedBy ?? "unknown"}. SHA-256 of the sealed document: <span className={styles.mono}>{record.contentHash}</span>
          </p>
          <p className={styles.note}>
            {sealedVerified(record.sealedDocument)
              ? "Every decision and the approval are signed with a verified re-authentication."
              : "Not every signature rests on a verified re-authentication, so this is not a 21 CFR Part 11 signed record."}
          </p>
          <p className={styles.note}>
            <a href={batchRecordPdfUrl(record.id)}>Download the PDF</a> (rendered from the sealed document; not PDF/A yet).
          </p>
          {record.sealedDocument ? (
            <details>
              <summary>Sealed document</summary>
              <pre className={styles.mono}>{record.sealedDocument}</pre>
            </details>
          ) : null}
        </Card>
      ) : null}
      <DataTable
        label="Exceptions"
        columns={columns}
        rows={exceptions}
        getRowKey={(e) => e.id}
        empty={{ title: "No exceptions", description: "Every recorded value is within its limits and nothing was raised by hand." }}
      />
      {chosen && canWrite && inReview && !chosen.disposition ? (
        <Card title={`Decide: ${chosen.description}`}>
          <Alert tone="info" title="Electronic signature">
            {SIGNING_NOTE} A decision is final once recorded.
          </Alert>
          <form className={styles.form} onSubmit={decide} noValidate>
            <FormField id="exc-disposition" label="Decision">
              {(c) => (
                <Select {...c} value={disposition} onChange={(e) => setDisposition(e.target.value as RecordDisposition)}>
                  <option value="accepted">Accept</option>
                  <option value="deviation-raised">Raise a deviation</option>
                  <option value="rejected">Reject</option>
                </Select>
              )}
            </FormField>
            <FormField id="exc-signature" label="Printed name" hint="Your name as it appears on the signature." error={errors.printedName}>
              {(c) => <Input {...c} value={printedName} onChange={(e) => setPrintedName(e.target.value)} invalid={Boolean(errors.printedName)} />}
            </FormField>
            <FormField id="exc-comment" label="Comment" error={errors.comment}>
              {(c) => <Input {...c} value={comment} onChange={(e) => setComment(e.target.value)} invalid={Boolean(errors.comment)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "decide"}>
                Record decision
              </Button>
              <Button onClick={() => setSelected(null)}>Cancel</Button>
            </div>
          </form>
        </Card>
      ) : null}
      {canWrite && inReview ? (
        <Card title="Raise an exception">
          <form className={styles.form} onSubmit={raise} noValidate>
            <FormField id="exc-text" label="What was found" error={errors.text}>
              {(c) => <Input {...c} value={text} onChange={(e) => setText(e.target.value)} invalid={Boolean(errors.text)} />}
            </FormField>
            <FormField id="exc-severity" label="Severity">
              {(c) => (
                <Select {...c} value={severity} onChange={(e) => setSeverity(e.target.value as ExceptionSeverity)}>
                  <option value="minor">Minor</option>
                  <option value="major">Major</option>
                  <option value="critical">Critical</option>
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy === "raise"}>
                Raise exception
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <div className={styles.formActions}>
        {canWrite && inReview ? (
          <Button onClick={() => void run("compile", (k) => compileBatchRecord(record.batchId, k))} loading={busy === "compile"}>
            Compile again
          </Button>
        ) : null}
        {canApprove && inReview ? (
          <>
            <FormField id="approve-name" label="Printed name for the approval" hint={SIGNING_NOTE} error={errors.approveName}>
              {(c) => <Input {...c} value={printedName} onChange={(e) => setPrintedName(e.target.value)} invalid={Boolean(errors.approveName)} />}
            </FormField>
            <Button
              variant="primary"
              onClick={() => {
                if (printedName.trim() === "" || printedName.trim().length > 200) {
                  setErrors({ approveName: "Enter your printed name to sign." });
                  return;
                }
                setErrors({});
                void run("approve", (k) => approveBatchRecord(record.id, record.version, printedName.trim(), k));
              }}
              loading={busy === "approve"}
            >
              Approve record
            </Button>
          </>
        ) : null}
        {canApprove && record.status === "approved" ? (
          <Button variant="primary" onClick={() => void run("seal", (k) => sealBatchRecord(record.id, record.version, k))} loading={busy === "seal"}>
            Seal record
          </Button>
        ) : null}
        <Link className={buttonClassName("ghost")} to="/processmfg/batch-records">
          Back to the list
        </Link>
      </div>
    </div>
  );
}

/** FR-24.21: the profiles that say which items get a batch record and whether release needs it approved. */
export function RecordProfilesPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), RECORD_WRITE_DUTY);
  const [itemId, setItemId] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const item = itemId.trim();
  const filterError = item !== "" && !UUID.test(item);
  const state = useLoad((signal) => (filterError ? Promise.resolve([]) : listRecordProfiles({ itemId: item }, signal)), `profiles|${item}|${filterError}`);

  async function retire(profile: RecordProfile) {
    setBusy(profile.id);
    setFailure(null);
    try {
      await retireRecordProfile(profile.id, profile.version);
      state.reload();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<RecordProfile>[] = [
    { key: "code", header: "Code", mono: true, render: (p) => p.code },
    { key: "itemId", header: "Item", mono: true, render: (p) => shortId(p.itemId) },
    { key: "approvalRequiredForRelease", header: "Release needs approved record", render: (p) => (p.approvalRequiredForRelease ? "Yes" : "No") },
    { key: "retiredAt", header: "Status", render: (p) => (p.retiredAt ? <Badge tone="neutral">Retired</Badge> : <Badge tone="success">Active</Badge>) },
    {
      key: "id",
      header: "Action",
      render: (p) =>
        canWrite && !p.retiredAt ? (
          <Button size="sm" variant="danger" loading={busy === p.id} onClick={() => void retire(p)}>
            {`Retire ${p.code}`}
          </Button>
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch record profiles"
        description="A profile says that the batches of an item get an electronic batch record, and whether a batch can be released only with the record approved."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/processmfg/batch-record-profiles/new">
              Define profile
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="profile-filter-item" label="Item id" error={filterError ? "Enter the item id as a UUID." : undefined}>
          {(c) => <Input {...c} value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={filterError} />}
        </FormField>
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not retire the profile" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <LoadError title="Could not load the profiles" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Batch record profiles"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading}
          empty={
            item
              ? { title: "No profiles match", description: "Clear the filter to see every profile." }
              : { title: "No batch record profiles yet", description: "Items without a profile are released without a batch record." }
          }
        />
      )}
    </div>
  );
}

/** Defines the batch record profile of an item. */
export function NewRecordProfilePage() {
  const navigate = useNavigate();
  const allowed = holdsDuty(usePrincipalDuties(), RECORD_WRITE_DUTY);
  const [code, setCode] = useState("");
  const [itemId, setItemId] = useState("");
  const [required, setRequired] = useState(true);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function on<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v);
      key.current = newKey();
    };
  }

  if (!allowed) {
    return <Missing title="Define profile">Defining batch record profiles needs the batch record duty. Ask your administrator if you should have it.</Missing>;
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!/^[A-Z0-9][A-Z0-9_.-]{0,39}$/.test(code.trim())) found.code = "Use 1 to 40 upper-case letters, digits, '_', '.' or '-'.";
    if (!UUID.test(itemId.trim())) found.itemId = "Enter the item id as a UUID.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await defineRecordProfile({ code: code.trim(), itemId: itemId.trim(), approvalRequiredForRelease: required }, key.current);
      navigate("/processmfg/batch-record-profiles");
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Define profile" description="An item has one active profile. While it asks for approval, a batch of the item is registered or released as available only with an approved record." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="profile-code" label="Code" error={errors.code}>
            {(c) => <Input {...c} value={code} onChange={(e) => on(setCode)(e.target.value)} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="profile-item" label="Item id" error={errors.itemId}>
            {(c) => <Input {...c} value={itemId} onChange={(e) => on(setItemId)(e.target.value)} invalid={Boolean(errors.itemId)} />}
          </FormField>
          <div className={styles.lineChecks}>
            <label>
              <input type="checkbox" checked={required} onChange={(e) => on(setRequired)(e.target.checked)} /> Releasing a batch needs the approved record
            </label>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not define the profile" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Define profile
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/batch-record-profiles">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** Reads signaturesVerified from the sealed JSON document; anything unreadable counts as unverified. */
function sealedVerified(doc: string | undefined): boolean {
  if (!doc) return false;
  try {
    return (JSON.parse(doc) as { signaturesVerified?: unknown }).signaturesVerified === true;
  } catch {
    return false;
  }
}

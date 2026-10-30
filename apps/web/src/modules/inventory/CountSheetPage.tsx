import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, EmptyState, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  approveCountSheet,
  cancelCountSheet,
  describeProblem,
  enterCount,
  getCountSheet,
  postCountSheet,
  requestCountRecount,
  submitCountSheet,
  type CountLine,
  type CountSheet,
  type Problem,
} from "./api/client";
import { dash, DECIMAL, describeDimensions, formatDateTime, sentence, shortId, signed } from "./format";
import { ALL_DIMENSIONS, Detail, ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Inline count entry for one line; saves at the sheet version the screen holds. */
function CountEntry({ sheet, line, onSaved, onError }: { sheet: CountSheet; line: CountLine; onSaved(next: CountSheet): void; onError(p: Problem): void }) {
  const [value, setValue] = useState(line.countedQty ?? "");
  const [invalid, setInvalid] = useState(false);
  const [busy, setBusy] = useState(false);

  async function save(event: FormEvent) {
    event.preventDefault();
    const qty = value.trim();
    if (!DECIMAL.test(qty) || Number(qty) < 0) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setBusy(true);
    try {
      onSaved(await enterCount(sheet.id, line.id, sheet.version, qty));
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} noValidate className={styles.inline}>
      <Input
        aria-label={`Counted quantity, line ${line.lineNo}`}
        inputMode="decimal"
        value={value}
        onChange={(event) => setValue(event.target.value)}
        invalid={invalid}
        className={styles.narrow}
      />
      <Button type="submit" size="sm" loading={busy} aria-label={`Save count, line ${line.lineNo}`}>
        Save
      </Button>
    </form>
  );
}

/** Supervisor's recount request for one counted line: a reason, then the sheet reopens. */
function RecountRequest({ sheet, line, onDone, onError }: { sheet: CountSheet; line: CountLine; onDone(next: CountSheet): void; onError(p: Problem): void }) {
  const [shown, setShown] = useState(false);
  const [reason, setReason] = useState("");
  const [invalid, setInvalid] = useState(false);
  const [busy, setBusy] = useState(false);

  async function send(event: FormEvent) {
    event.preventDefault();
    if (reason.trim() === "" || reason.trim().length > 500) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setBusy(true);
    try {
      onDone(await requestCountRecount(sheet.id, line.id, sheet.version, reason.trim()));
      setShown(false);
      setReason("");
    } catch (error) {
      onError(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (!shown) {
    return (
      <Button size="sm" onClick={() => setShown(true)} aria-label={`Request recount, line ${line.lineNo}`}>
        Recount
      </Button>
    );
  }
  return (
    <form onSubmit={send} noValidate className={styles.inline}>
      <Input aria-label={`Recount reason, line ${line.lineNo}`} value={reason} maxLength={500} onChange={(event) => setReason(event.target.value)} invalid={invalid} />
      <Button type="submit" size="sm" loading={busy} aria-label={`Send recount request, line ${line.lineNo}`}>
        Send
      </Button>
    </form>
  );
}

/** Count sheet (PRD 3.16.6): enter counted quantities, then post the variances. */
export function CountSheetPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCountSheet(id, signal), id);
  const [sheet, setSheet] = useState<CountSheet | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [posting, setPosting] = useState(false);
  const [acting, setActing] = useState("");
  const [confirmCancel, setConfirmCancel] = useState(false);

  useEffect(() => {
    setSheet(state.data);
  }, [state.data]);

  async function post() {
    if (!sheet) return;
    setPosting(true);
    setFailure(null);
    setNotice("");
    try {
      const posted = await postCountSheet(sheet.id, sheet.version);
      setSheet(posted);
      setNotice(posted.journalId ? `Count posted. Variances went to count journal ${shortId(posted.journalId)}.` : "Count posted. Every counted line matched the book quantity.");
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setPosting(false);
    }
  }

  async function move(name: string, run: (id: string, version: number) => Promise<CountSheet>, done: string) {
    if (!sheet) return;
    setActing(name);
    setFailure(null);
    setNotice("");
    try {
      setSheet(await run(sheet.id, sheet.version));
      setNotice(done);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setActing("");
    }
  }

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/count-sheets">
      Back to count sheets
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-CNT-004") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Count sheet not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the count sheet" problem={state.error} onRetry={state.reload} />;
  }
  if (!sheet) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading count sheet…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const open = sheet.status === "OPEN";
  const reviewable = open || sheet.status === "COUNTED" || sheet.status === "APPROVED";
  const canPost = reviewable && (!sheet.fourEyes || sheet.status === "APPROVED");
  const hidden = sheet.lines.some((l) => l.expectedQty === null);
  const columns: DataTableColumn<CountLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
    { key: "expectedQty", header: "Expected", align: "end", mono: true, render: (row) => row.expectedQty ?? "Hidden" },
    {
      key: "countedQty",
      header: "Counted",
      render: (row) =>
        open ? (
          <CountEntry
            key={`${row.id}-${row.countedQty ?? ""}`}
            sheet={sheet}
            line={row}
            onSaved={(next) => {
              setFailure(null);
              setSheet(next);
              setNotice(`Line ${row.lineNo} counted.`);
            }}
            onError={setFailure}
          />
        ) : (
          row.countedQty ?? "Not counted"
        ),
    },
    { key: "postedVariance", header: "Variance", align: "end", mono: true, render: (row) => (row.postedVariance === null ? dash : signed(row.postedVariance)) },
    {
      key: "recount",
      header: "Recount",
      render: (row) =>
        row.recountRequested ? (
          <Badge tone="warning">{`Recount: ${row.recountReason ?? "requested"}`}</Badge>
        ) : reviewable && row.countedQty !== null ? (
          <RecountRequest
            key={`${row.id}-${sheet.version}`}
            sheet={sheet}
            line={row}
            onDone={(next) => {
              setFailure(null);
              setSheet(next);
              setNotice(`Line ${row.lineNo} sent back for recount. The sheet is open again.`);
            }}
            onError={setFailure}
          />
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Count sheet ${sheet.number}`}
        meta={<Badge tone={stateTones[sheet.status] ?? "neutral"}>{sentence(sheet.status)}</Badge>}
        description={`${sheet.planName} · ${sheet.countedCount} of ${sheet.lineCount} lines counted`}
        actions={
          <>
            {back}
            {reviewable ? (
              <Button loading={acting === "cancel"} onClick={() => setConfirmCancel(true)}>
                Cancel sheet
              </Button>
            ) : null}
            {open ? (
              <Button
                loading={acting === "submit"}
                disabled={sheet.countedCount === 0 || sheet.recountLines > 0}
                onClick={() => move("submit", submitCountSheet, "Count submitted for review.")}
              >
                Submit for review
              </Button>
            ) : null}
            {sheet.status === "COUNTED" ? (
              <Button variant={sheet.fourEyes ? "primary" : undefined} loading={acting === "approve"} onClick={() => move("approve", approveCountSheet, "Count approved.")}>
                Approve count
              </Button>
            ) : null}
            {canPost ? (
              <Button variant="primary" loading={posting} disabled={sheet.countedCount === 0 || sheet.recountLines > 0} onClick={post}>
                Post count
              </Button>
            ) : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {confirmCancel ? (
        <ConfirmDialog
          title="Cancel this count sheet?"
          description={`${sheet.number} will be closed without posting. Its counts are kept for reference but can no longer be posted${sheet.freezeStock ? ", and the stock freeze is lifted" : ""}.`}
          confirmLabel="Cancel sheet"
          cancelLabel="Keep sheet"
          onCancel={() => setConfirmCancel(false)}
          onConfirm={() => {
            setConfirmCancel(false);
            void move("cancel", cancelCountSheet, sheet.freezeStock ? "Count sheet cancelled. Its stock is no longer frozen." : "Count sheet cancelled.");
          }}
        />
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={failure.code === "INV-CNT-001" ? "Supervisor approval required" : "The count was not saved or posted"}
          live="alert"
          action={failure.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload sheet</Button> : undefined}
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {sheet.freezeStock && reviewable ? (
        <Alert tone="warning" title="Stock is frozen">
          Journals, transfers, blockings and receipts or issues that move the counted rows are refused (INV-CNT-002) until this sheet is posted or cancelled.
        </Alert>
      ) : null}
      {sheet.recountLines > 0 ? (
        <Alert tone="warning" title="Recount requested">
          {sheet.recountLines} line(s) must be entered again before the sheet can be submitted or posted.
        </Alert>
      ) : null}
      {sheet.fourEyes && reviewable && sheet.status !== "APPROVED" ? (
        <Alert tone="info" title="Separate approval required">
          Submit the sheet, then a supervisor who did not count must approve it before it can post.
        </Alert>
      ) : null}
      {hidden ? (
        <Alert tone="info" title="Blind count">
          Expected quantities are hidden until the sheet is posted. Count what is on the shelf.
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Detail term="Plan">{sheet.planName}</Detail>
          <Detail term="Legal entity" mono>
            {sheet.legalEntityId}
          </Detail>
          <Detail term="Generated">{formatDateTime(sheet.createdAt)}</Detail>
          <Detail term="Posted">{formatDateTime(sheet.postedAt)}</Detail>
          <Detail term="Submitted by" mono>
            {sheet.submittedBy || dash}
          </Detail>
          <Detail term="Approved by" mono>
            {sheet.approvedBy || dash}
          </Detail>
          <Detail term="Count journal" mono>
            {sheet.journalId || dash}
          </Detail>
        </dl>
        {sheet.journalId ? (
          <p className={styles.muted}>
            <Link to={`/inventory/journals/${sheet.journalId}`}>Open the count journal</Link>
          </p>
        ) : null}
      </Card>
      <DataTable
        label="Count lines"
        columns={columns}
        rows={sheet.lines}
        getRowKey={(row) => row.id}
        empty={{ title: "Nothing to count", description: "The plan's warehouse held no stock when the sheet was generated." }}
      />
    </div>
  );
}

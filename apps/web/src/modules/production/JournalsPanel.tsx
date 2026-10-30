import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  createJournal,
  deleteJournal,
  describeProblem,
  journalTypesFor,
  listJournals,
  newIdempotencyKey,
  postJournal,
  type CreateJournalInput,
  type Journal,
  type JournalLine,
  type JournalType,
  type Posting,
  type Problem,
  type ProductionOrder,
  type Proposal,
  type TriggerFiring,
} from "./api/client";
import { addDecimals, amount, dash, describeFiring, isZero, journalTypeLabel, proposalLabel, triggerFunctionLabel } from "./labels";
import styles from "./pages.module.css";

const quantityPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;
const hoursPattern = /^[0-9]{1,8}(\.[0-9]{1,2})?$/;
const positive = (v: string) => /[1-9]/.test(v);

export interface JournalsPanelProps {
  order: ProductionOrder;
  /** Called after a post changed the order (consumption, status, costs). */
  onChanged(): void;
  /** Opens another order: the counterpart of a split-transfer journal. */
  onOpenOrder?(orderId: string): void;
}

export interface Draft {
  type: JournalType | "";
  proposal: Proposal;
  postingDate: string;
  quantity: string;
  errorQuantity: string;
  /** Manual lines: quantity and unit cost by BOM line; hours, good and error quantity by operation. */
  issues: Record<string, { quantity: string; unitCost: string }>;
  hours: Record<string, { hours: string; qtyGood: string; qtyError?: string }>;
}

export const emptyDraft = (type: JournalType | ""): Draft => ({
  type,
  proposal: type === "report-as-finished" ? "proportional" : "remaining",
  postingDate: "",
  quantity: "",
  errorQuantity: "",
  issues: {},
  hours: {},
});

type Errors = Record<string, string>;

/** Validates the journal form like the service and builds the CreateJournal body. */
export function toJournalInput(draft: Draft): { input?: CreateJournalInput; errors: Errors } {
  const errors: Errors = {};
  if (!draft.type) {
    errors.type = "Choose a journal type.";
    return { errors };
  }
  const input: CreateJournalInput = { type: draft.type, proposal: draft.proposal };
  if (draft.postingDate) {
    input.postingDate = draft.postingDate;
  }
  if (draft.type === "report-as-finished") {
    if (!quantityPattern.test(draft.quantity) || !positive(draft.quantity)) {
      errors.quantity = "Enter a positive quantity with up to 6 decimals.";
    }
    input.quantity = draft.quantity;
    if (draft.errorQuantity !== "") {
      if (!quantityPattern.test(draft.errorQuantity)) {
        errors.errorQuantity = "Enter a quantity with up to 6 decimals, or leave it empty.";
      }
      input.errorQuantity = draft.errorQuantity;
    }
  }
  if (draft.proposal === "none") {
    if (draft.type === "route-card") {
      input.hours = [];
      for (const [oper, line] of Object.entries(draft.hours)) {
        const qtyError = line.qtyError ?? "";
        if (line.hours === "" && line.qtyGood === "" && qtyError === "") {
          continue;
        }
        if (line.hours !== "" && (!hoursPattern.test(line.hours) || !positive(line.hours))) {
          errors[`hours.${oper}`] = "Enter at least 0.01 hours with up to 2 decimals.";
        }
        if (line.qtyGood !== "" && !quantityPattern.test(line.qtyGood)) {
          errors[`good.${oper}`] = "Enter a quantity with up to 6 decimals.";
        }
        if (qtyError !== "" && !quantityPattern.test(qtyError)) {
          errors[`error.${oper}`] = "Enter a quantity with up to 6 decimals.";
        }
        input.hours.push({
          operNumber: Number(oper),
          hours: line.hours || "0",
          ...(line.qtyGood ? { qtyGood: line.qtyGood } : {}),
          ...(qtyError ? { qtyError } : {}),
        });
      }
      if (input.hours.length === 0) {
        errors.lines = "Enter hours, a good or an error quantity for at least one operation.";
      }
    } else {
      input.issues = [];
      for (const [lineNo, line] of Object.entries(draft.issues)) {
        if (line.quantity === "") {
          continue;
        }
        if (!quantityPattern.test(line.quantity) || !positive(line.quantity)) {
          errors[`qty.${lineNo}`] = "Enter a positive quantity with up to 6 decimals.";
        }
        if (line.unitCost !== "" && !quantityPattern.test(line.unitCost)) {
          errors[`cost.${lineNo}`] = "Enter a unit cost with up to 6 decimals.";
        }
        input.issues.push({ lineNo, quantity: line.quantity, ...(line.unitCost ? { unitCost: line.unitCost } : {}) });
      }
      if (input.issues.length === 0 && draft.type === "picking-list") {
        errors.lines = "Enter a quantity for at least one component.";
      }
    }
  }
  return { input, errors };
}

/** The value of a posted journal: the sum of its debits (the postings balance). */
const journalValue = (j: Journal) => addDecimals(j.postings.map((p) => p.dr));

const lineColumns: DataTableColumn<JournalLine>[] = [
  { key: "kind", header: "Line", render: (l) => (l.kind === "issue" ? `Issue ${l.lineNo}` : l.kind === "hours" ? `Operation ${l.operNumber}` : "Finished receipt") },
  { key: "itemId", header: "Item", mono: true, render: (l) => l.itemId ?? dash },
  { key: "qty", header: "Quantity or hours", align: "end" },
  { key: "qtyGood", header: "Good", align: "end", render: (l) => (l.kind === "issue" ? dash : l.qtyGood) },
  { key: "qtyError", header: "Error", align: "end", render: (l) => (l.kind === "issue" ? dash : l.qtyError) },
  { key: "unitCost", header: "Unit cost", align: "end" },
  { key: "amount", header: "Amount", align: "end", render: (l) => amount(l.amount) },
  { key: "overheadAmount", header: "Overhead", align: "end", render: (l) => (l.kind === "hours" ? amount(l.overheadAmount) : dash) },
];

const firingColumns: DataTableColumn<TriggerFiring>[] = [
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "function", header: "Trigger", render: (f) => triggerFunctionLabel[f.function] ?? f.function },
  { key: "status", header: "Result", render: (f) => <Badge tone={f.status === "fired" ? "success" : "warning"}>{f.status === "fired" ? "Fired" : "Failed"}</Badge> },
  { key: "outcome", header: "What happened", render: (f) => describeFiring(f) },
];

/** The notice after a post: the journal, and what its trigger points did (FR-21.17). */
export function postedNotice(journalNumber: string, firings: TriggerFiring[] = []): string {
  const fired = firings.filter((f) => f.status === "fired");
  if (fired.length === 0) {
    return `Journal ${journalNumber} posted.`;
  }
  return `Journal ${journalNumber} posted. ${fired.map((f) => `Operation ${f.operNumber}: ${describeFiring(f)}.`).join(" ")}`;
}

const postingColumns: DataTableColumn<Posting>[] = [
  { key: "postingType", header: "Posting type" },
  { key: "account", header: "Account", mono: true },
  { key: "dr", header: "Debit", align: "end", render: (p) => (isZero(p.dr) ? dash : amount(p.dr)) },
  { key: "cr", header: "Credit", align: "end", render: (p) => (isZero(p.cr) ? dash : amount(p.cr)) },
];

/** Journals of an order (PRD 4.21.6 Journals tab and journal editor): list, create with a proposal or by hand, post, delete drafts. */
export function JournalsPanel({ order, onChanged, onOpenOrder }: JournalsPanelProps) {
  const types = useMemo(() => journalTypesFor(order.status), [order.status]);
  const [journals, setJournals] = useState<{ rows: Journal[]; loading: boolean; error: Problem | null }>({ rows: [], loading: true, error: null });
  const [draft, setDraft] = useState<Draft>(() => emptyDraft(types[0] ?? ""));
  const [errors, setErrors] = useState<Errors>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState<"create" | "post" | "delete" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [triggerFailures, setTriggerFailures] = useState<TriggerFiring[]>([]);
  const [confirmDelete, setConfirmDelete] = useState(false);
  // One Idempotency-Key per create body and per journal post, so a retry replays.
  const keys = useRef(new Map<string, string>());
  const keyFor = (slot: string) => {
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    return key;
  };

  const load = useCallback(
    (signal?: AbortSignal) =>
      listJournals(order.id, signal).then(
        (rows) => setJournals({ rows, loading: false, error: null }),
        (err) => !signal?.aborted && setJournals({ rows: [], loading: false, error: describeProblem(err) }),
      ),
    [order.id],
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load, order.version]);

  useEffect(() => {
    if (draft.type && !types.includes(draft.type)) {
      setDraft(emptyDraft(types[0] ?? ""));
    }
  }, [types, draft.type]);

  const current = journals.rows.find((j) => j.id === selected) ?? null;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { input, errors: found } = toJournalInput(draft);
    setErrors(found);
    if (!input || Object.keys(found).length > 0) {
      return;
    }
    setBusy("create");
    setFailure(null);
    setNotice("");
    try {
      const created = await createJournal(order.id, input, keyFor(`create:${order.version}:${JSON.stringify(input)}`));
      setNotice(`Journal ${created.journalNumber} created with ${created.lines.length} lines. Review it, then post it.`);
      setDraft(emptyDraft(draft.type));
      setSelected(created.id);
      await load();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const post = async (journal: Journal) => {
    setBusy("post");
    setFailure(null);
    setNotice("");
    setTriggerFailures([]);
    try {
      const posted = await postJournal(journal.id, keyFor(`post:${journal.id}`));
      setNotice(postedNotice(posted.journalNumber, posted.triggerFirings));
      setTriggerFailures((posted.triggerFirings ?? []).filter((f) => f.status === "failed"));
      await load();
      onChanged();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (journal: Journal) => {
    setBusy("delete");
    setFailure(null);
    setNotice("");
    try {
      await deleteJournal(journal.id);
      setNotice(`Draft ${journal.journalNumber} deleted.`);
      setSelected(null);
      setConfirmDelete(false);
      await load();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const columns: DataTableColumn<Journal>[] = [
    { key: "journalNumber", header: "Journal", mono: true },
    { key: "type", header: "Type", render: (j) => journalTypeLabel[j.type] },
    { key: "postingDate", header: "Posting date" },
    {
      key: "posted",
      header: "Status",
      render: (j) =>
        j.reversalOf ? (
          <Badge tone="warning">Reversal</Badge>
        ) : j.reversedBy ? (
          <Badge tone="neutral">Reversed</Badge>
        ) : j.posted ? (
          <Badge tone="success">Posted</Badge>
        ) : (
          <Badge tone="info">Draft</Badge>
        ),
    },
    { key: "lines", header: "Lines", align: "end", render: (j) => String(j.lines.length) },
    { key: "value", header: "Value", align: "end", render: (j) => (j.posted ? `${amount(journalValue(j))} ${order.currency}` : dash) },
  ];

  const manual = draft.proposal === "none";
  const setIssue = (lineNo: string, patch: Partial<{ quantity: string; unitCost: string }>) =>
    setDraft({ ...draft, issues: { ...draft.issues, [lineNo]: { ...(draft.issues[lineNo] ?? { quantity: "", unitCost: "" }), ...patch } } });
  const setHours = (oper: number, patch: Partial<{ hours: string; qtyGood: string; qtyError: string }>) =>
    setDraft({ ...draft, hours: { ...draft.hours, [oper]: { ...(draft.hours[oper] ?? { hours: "", qtyGood: "", qtyError: "" }), ...patch } } });

  return (
    <Card title="Journals">
      <div className={styles.stack}>
        {notice ? (
          <Alert tone="success" live="status">
            {notice}
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" live="alert" title="The journal was refused">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {triggerFailures.length > 0 ? (
          <Alert tone="warning" live="alert" title="A trigger point did nothing; the journal was posted">
            {triggerFailures.map((f) => `Operation ${f.operNumber}, ${triggerFunctionLabel[f.function] ?? f.function}: ${f.reason ?? ""}`).join(" ")} Fix the
            trigger point; it acts again on the next feedback.
          </Alert>
        ) : null}
        {journals.error ? (
          <Alert tone="danger" title="Could not load the journals">
            {journals.error.message} ({journals.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Production journals"
            columns={columns}
            rows={journals.rows}
            loading={journals.loading}
            getRowKey={(j) => j.id}
            onRowOpen={(j) => {
              setSelected(j.id);
              setConfirmDelete(false);
            }}
            empty={{ title: "No journals yet", description: "Picking lists, route cards and reports as finished posted on this order appear here." }}
          />
        )}

        {current ? (
          <section className={styles.stack} aria-label={`Journal ${current.journalNumber}`}>
            <div className={styles.toolbar}>
              <p className={styles.hint}>
                {journalTypeLabel[current.type]} {current.journalNumber}, posting date {current.postingDate}
                {current.posted ? `, posted by ${current.postedBy ?? dash}` : ", not posted yet"}.
              </p>
              {!current.posted ? (
                <div className={styles.actions}>
                  <Button variant="primary" loading={busy === "post"} onClick={() => void post(current)}>
                    Post journal
                  </Button>
                  <Button variant="danger" onClick={() => setConfirmDelete(true)}>
                    Delete draft
                  </Button>
                </div>
              ) : null}
            </div>
            {current.type === "split-transfer" && current.counterpartOrderId ? (
              <div className={styles.toolbar}>
                <p className={styles.hint}>
                  {current.counterpartOrderId === order.splitFromOrderId
                    ? "Moves this order's share of the consumption, hours and WIP posted on the order it was split from."
                    : "Moves the share of the consumption, hours and WIP posted here to the order split off from this one."}
                </p>
                {onOpenOrder ? (
                  <Button variant="ghost" size="sm" onClick={() => onOpenOrder(current.counterpartOrderId!)}>
                    Open the other order
                  </Button>
                ) : null}
              </div>
            ) : null}
            {confirmDelete ? (
              <Alert
                tone="warning"
                title={`Delete draft ${current.journalNumber}?`}
                action={
                  <div className={styles.actions}>
                    <Button variant="danger" loading={busy === "delete"} onClick={() => void remove(current)}>
                      Yes, delete the draft
                    </Button>
                    <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
                      Keep it
                    </Button>
                  </div>
                }
              >
                The draft and its lines are removed; nothing was posted.
              </Alert>
            ) : null}
            <DataTable label={`Lines of ${current.journalNumber}`} columns={lineColumns} rows={current.lines} getRowKey={(_, i) => String(i)} />
            {current.posted ? (
              <DataTable
                label={`Postings of ${current.journalNumber}`}
                columns={postingColumns}
                rows={current.postings}
                getRowKey={(_, i) => String(i)}
                empty={{ title: "No postings", description: "A journal whose lines are all zero posts nothing." }}
              />
            ) : null}
            {current.triggerFirings && current.triggerFirings.length > 0 ? (
              <DataTable
                label={`Trigger points fired by ${current.journalNumber}`}
                columns={firingColumns}
                rows={current.triggerFirings}
                getRowKey={(f) => f.id}
              />
            ) : null}
          </section>
        ) : null}

        {types.length === 0 ? (
          <p className={styles.hint}>
            Journals are posted on Released (route cards), Started and Reported-as-finished orders; this order is {order.status}.
          </p>
        ) : (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="New journal">
            <div className={styles.fields}>
              <FormField id="journal-type" label="Journal type" error={errors.type}>
                {(control) => (
                  <Select {...control} value={draft.type} onChange={(e) => setDraft(emptyDraft(e.target.value as JournalType))}>
                    {types.map((t) => (
                      <option key={t} value={t}>
                        {journalTypeLabel[t]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="journal-proposal" label="Lines">
                {(control) => (
                  <Select {...control} value={draft.proposal} onChange={(e) => setDraft({ ...draft, proposal: e.target.value as Proposal })}>
                    {(["remaining", "proportional", "none"] as Proposal[]).map((p) => (
                      <option key={p} value={p}>
                        {proposalLabel[p]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="journal-date" label="Posting date" optional hint="Defaults to today.">
                {(control) => <Input {...control} type="date" value={draft.postingDate} onChange={(e) => setDraft({ ...draft, postingDate: e.target.value })} />}
              </FormField>
              {draft.type === "report-as-finished" ? (
                <>
                  <FormField id="journal-quantity" label="Good quantity" error={errors.quantity}>
                    {(control) => <Input {...control} inputMode="decimal" value={draft.quantity} onChange={(e) => setDraft({ ...draft, quantity: e.target.value })} />}
                  </FormField>
                  <FormField id="journal-error-quantity" label="Error quantity" optional error={errors.errorQuantity}>
                    {(control) => (
                      <Input {...control} inputMode="decimal" value={draft.errorQuantity} onChange={(e) => setDraft({ ...draft, errorQuantity: e.target.value })} />
                    )}
                  </FormField>
                </>
              ) : null}
            </div>
            {manual && draft.type === "route-card" ? (
              <div className={styles.rows}>
                {order.operations
                  .filter((op) => !op.subcontract && op.status !== "finished")
                  .map((op) => (
                    <fieldset key={op.operNumber} className={styles.row}>
                      <legend className={styles.legend}>Operation {op.operNumber}</legend>
                      <FormField id={`journal-hours-${op.operNumber}`} label="Hours" error={errors[`hours.${op.operNumber}`]}>
                        {(control) => (
                          <Input {...control} inputMode="decimal" value={draft.hours[op.operNumber]?.hours ?? ""} onChange={(e) => setHours(op.operNumber, { hours: e.target.value })} />
                        )}
                      </FormField>
                      <FormField id={`journal-good-${op.operNumber}`} label="Good quantity" optional error={errors[`good.${op.operNumber}`]}>
                        {(control) => (
                          <Input {...control} inputMode="decimal" value={draft.hours[op.operNumber]?.qtyGood ?? ""} onChange={(e) => setHours(op.operNumber, { qtyGood: e.target.value })} />
                        )}
                      </FormField>
                      <FormField id={`journal-error-${op.operNumber}`} label="Error quantity" optional error={errors[`error.${op.operNumber}`]}>
                        {(control) => (
                          <Input
                            {...control}
                            inputMode="decimal"
                            value={draft.hours[op.operNumber]?.qtyError ?? ""}
                            onChange={(e) => setHours(op.operNumber, { qtyError: e.target.value })}
                          />
                        )}
                      </FormField>
                    </fieldset>
                  ))}
              </div>
            ) : null}
            {manual && draft.type !== "route-card" ? (
              <div className={styles.rows}>
                {order.lines
                  .filter((l) => draft.type !== "report-as-finished" || l.flushing === "finish")
                  .map((l) => (
                    <fieldset key={l.lineNo} className={styles.row}>
                      <legend className={styles.legend}>
                        Component {l.lineNo} ({l.qtyConsumed ?? "0"} of {l.qtyRequired} issued)
                      </legend>
                      <FormField id={`journal-qty-${l.lineNo}`} label="Quantity" error={errors[`qty.${l.lineNo}`]}>
                        {(control) => (
                          <Input {...control} inputMode="decimal" value={draft.issues[l.lineNo]?.quantity ?? ""} onChange={(e) => setIssue(l.lineNo, { quantity: e.target.value })} />
                        )}
                      </FormField>
                      <FormField id={`journal-cost-${l.lineNo}`} label="Unit cost" optional hint={`Estimated ${l.unitCost}`} error={errors[`cost.${l.lineNo}`]}>
                        {(control) => (
                          <Input {...control} inputMode="decimal" value={draft.issues[l.lineNo]?.unitCost ?? ""} onChange={(e) => setIssue(l.lineNo, { unitCost: e.target.value })} />
                        )}
                      </FormField>
                    </fieldset>
                  ))}
              </div>
            ) : null}
            {errors.lines ? (
              <Alert tone="danger" live="alert">
                {errors.lines}
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "create"}>
                Create journal
              </Button>
            </div>
          </form>
        )}
      </div>
    </Card>
  );
}

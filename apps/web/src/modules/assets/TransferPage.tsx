import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getAsset,
  newKey,
  transferAsset,
  type Book,
  type NewTransferPart,
  type Problem,
  type Transfer,
  type TransferInput,
  type TransferKind,
  type TransferTarget,
} from "./api/client";
import { DATE, money, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface PartValues {
  assetNumber: string;
  name: string;
  percentage: string;
  legalEntityId: string;
  classCode: string;
}

export interface TransferValues {
  kind: TransferKind;
  transferDate: string;
  parts: PartValues[];
}

export type TransferErrors = { kind?: string; transferDate?: string; parts?: string; partFields?: Partial<Record<keyof PartValues, string>>[] };

/** The service's share rule: above 0, at most 100, at most two decimals. */
const SHARE = /^\d{1,3}(\.\d{1,2})?$/;
const CLASS = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
export const MAX_PARTS = 50;

/** "33.3" → 3330 hundredths, without floats. */
function hundredths(value: string): number {
  const [whole, frac = ""] = value.split(".");
  return Number.parseInt(whole, 10) * 100 + Number.parseInt(frac.padEnd(2, "0"), 10);
}

/** "10000" → "100.00". */
function fromHundredths(n: number): string {
  return `${Math.floor(n / 100)}.${String(n % 100).padStart(2, "0")}`;
}

/** Checks that mirror the service (domain.ValidateTransfer, api transfer): one target for a transfer, 2 to 50 for a split, shares totalling 100.00. */
export function validateTransfer(values: TransferValues): { errors: TransferErrors; input?: TransferInput } {
  const errors: TransferErrors = {};
  if (!DATE.test(values.transferDate)) errors.transferDate = "Choose the transfer date.";
  const count = values.parts.length;
  if (values.kind === "transfer" && count !== 1) errors.parts = "A transfer has exactly one target asset.";
  if (values.kind === "split" && (count < 2 || count > MAX_PARTS)) errors.parts = `A split has 2 to ${MAX_PARTS} target assets.`;
  const partFields: Partial<Record<keyof PartValues, string>>[] = [];
  const numbers = new Set<string>();
  let total = 0;
  const parts: NewTransferPart[] = values.parts.map((p, i) => {
    const f: Partial<Record<keyof PartValues, string>> = {};
    const number = p.assetNumber.trim();
    if (number.length < 1 || number.length > 40) f.assetNumber = "Enter an asset number of up to 40 characters.";
    else if (numbers.has(number)) f.assetNumber = "Another target already uses this number.";
    numbers.add(number);
    const name = p.name.trim();
    if (name.length < 1 || name.length > 120) f.name = "Enter a name of up to 120 characters.";
    const pct = p.percentage.trim();
    if (!SHARE.test(pct) || hundredths(pct) === 0 || hundredths(pct) > 10000) f.percentage = "Enter a share above 0 and at most 100, with up to two decimals.";
    else total += hundredths(pct);
    const le = p.legalEntityId.trim();
    if (le !== "" && !UUID.test(le)) f.legalEntityId = "Enter the legal entity UUID, or leave it empty.";
    const cls = p.classCode.trim();
    if (cls !== "" && !CLASS.test(cls)) f.classCode = "Use upper-case letters, digits, hyphen or underscore.";
    if (Object.keys(f).length > 0) partFields[i] = f;
    return {
      assetNumber: number,
      name,
      percentage: pct,
      ...(values.kind === "transfer" && le !== "" ? { legalEntityId: le } : {}),
      ...(cls !== "" ? { classCode: cls } : {}),
    };
  });
  if (partFields.length > 0) errors.partFields = partFields;
  if (partFields.every((f) => !f?.percentage) && !errors.parts && total !== 10000) {
    errors.parts = `The shares total ${fromHundredths(total)}%; they must total 100.00%.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { kind: values.kind, transferDate: values.transferDate, parts } };
}

function blankPart(percentage = ""): PartValues {
  return { assetNumber: "", name: "", percentage, legalEntityId: "", classCode: "" };
}

const bookColumns: DataTableColumn<Book>[] = [
  { key: "bookCode", header: "Book", mono: true },
  { key: "cost", header: "Cost", align: "end", render: (b) => money(b.cost, b.currency) },
  { key: "accumulatedDepreciation", header: "Accumulated", align: "end", render: (b) => money(b.accumulatedDepreciation, b.currency) },
  { key: "nbv", header: "NBV", align: "end", render: (b) => money(b.nbv, b.currency) },
  { key: "lastDepreciatedPeriod", header: "Depreciated to", mono: true, render: (b) => b.lastDepreciatedPeriod ?? "—" },
];

const targetColumns: DataTableColumn<TransferTarget>[] = [
  { key: "lineNo", header: "Asset", mono: true, render: (t) => <Link to={`/assets/${t.asset.id}`}>{t.asset.assetNumber}</Link> },
  { key: "percentage", header: "Share", align: "end", render: (t) => `${t.percentage}%` },
  {
    key: "books",
    header: "Cost per book",
    render: (t) => t.books.map((b) => `${b.bookCode} ${money(b.cost, b.currency)}`).join(", "),
  },
  {
    key: "asset",
    header: "NBV per book",
    render: (t) => t.books.map((b) => `${b.bookCode} ${money(b.nbv, b.currency)}`).join(", "),
  },
];

/** Transfer an asset to a new asset (for example in another legal entity) or split it by percentage (FR-37.7). */
export function TransferPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const [values, setValues] = useState<TransferValues>(() => ({ kind: "split", transferDate: today(), parts: [blankPart(), blankPart()] }));
  const [errors, setErrors] = useState<TransferErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<Transfer | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<TransferValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  function updatePart(index: number, patch: Partial<PartValues>) {
    update({ parts: values.parts.map((p, i) => (i === index ? { ...p, ...patch } : p)) });
  }
  function changeKind(kind: TransferKind) {
    update({ kind, parts: kind === "transfer" ? [{ ...(values.parts[0] ?? blankPart()), percentage: "100" }] : values.parts.length >= 2 ? values.parts : [...values.parts, blankPart()] });
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Transfer or split" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Transfer or split" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: asset, etag } = state.data;
  const openBooks = asset.books.filter((b) => b.status === "Open");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTransfer(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult((await transferAsset(id, etag, input, key.current)).value);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    return (
      <div className={styles.stack}>
        <PageHeader title={`${result.kind === "split" ? "Split" : "Transfer"} of ${asset.assetNumber}`} />
        <Alert tone="success" title={result.kind === "split" ? "Asset split" : "Asset transferred"} live="status">
          {`${asset.assetNumber} is now transferred. Its books moved to ${result.targets.length} new ${result.targets.length === 1 ? "asset" : "assets"}, which continue its depreciation.`}
        </Alert>
        <Card title="New assets">
          <DataTable label="New assets" columns={targetColumns} rows={result.targets} getRowKey={(t) => t.asset.id} />
        </Card>
        <div>
          <Link className={buttonClassName("primary")} to={`/assets/${id}`}>
            Back to {asset.assetNumber}
          </Link>
        </div>
      </div>
    );
  }

  const blocked = asset.status !== "Open" || openBooks.length === 0;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Transfer or split ${asset.assetNumber}`}
        description="Every open book moves. Cost, accumulated depreciation and salvage go to the new assets by share, with any rounding cents on the last one; the new books continue the depreciation schedule. The asset itself ends as transferred."
      />
      {blocked ? (
        <Alert tone="info" title="Nothing to transfer" live="status">
          {`The asset is ${asset.status === "NotYetAcquired" ? "not yet acquired" : asset.status.toLowerCase()}; only an open asset with an open book can be transferred or split.`}
        </Alert>
      ) : null}
      <Card title="Open books">
        <DataTable label="Open books" columns={bookColumns} rows={openBooks} getRowKey={(b) => b.id} empty={{ title: "No open books", description: "The asset has no open book to move." }} />
      </Card>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Transfer or split">
          <FormField id="transfer-kind" label="Kind" hint="A transfer moves the whole asset to one new asset; a split divides it into two or more.">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(e) => changeKind(e.target.value as TransferKind)}>
                <option value="split">Split</option>
                <option value="transfer">Transfer</option>
              </Select>
            )}
          </FormField>
          <FormField id="transfer-date" label="Transfer date" hint="On or after the end of the last depreciated period." error={errors.transferDate}>
            {(control) => (
              <Input {...control} type="date" value={values.transferDate} onChange={(e) => update({ transferDate: e.target.value })} invalid={Boolean(errors.transferDate)} />
            )}
          </FormField>
          {values.parts.map((p, i) => {
            const f = errors.partFields?.[i] ?? {};
            return (
              <fieldset key={i} className={styles.variant}>
                <legend className={styles.term}>{values.kind === "transfer" ? "New asset" : `New asset ${i + 1}`}</legend>
                <div className={styles.form}>
                  <FormField id={`part-${i}-assetNumber`} label="Asset number" error={f.assetNumber}>
                    {(control) => <Input {...control} value={p.assetNumber} onChange={(e) => updatePart(i, { assetNumber: e.target.value })} invalid={Boolean(f.assetNumber)} />}
                  </FormField>
                  <FormField id={`part-${i}-name`} label="Name" error={f.name}>
                    {(control) => <Input {...control} value={p.name} onChange={(e) => updatePart(i, { name: e.target.value })} invalid={Boolean(f.name)} />}
                  </FormField>
                  <FormField id={`part-${i}-percentage`} label="Share (%)" error={f.percentage}>
                    {(control) => (
                      <Input
                        {...control}
                        value={p.percentage}
                        readOnly={values.kind === "transfer"}
                        onChange={(e) => updatePart(i, { percentage: e.target.value })}
                        invalid={Boolean(f.percentage)}
                      />
                    )}
                  </FormField>
                  <FormField id={`part-${i}-classCode`} label="Class" hint={`Optional; defaults to ${asset.classCode}.`} error={f.classCode}>
                    {(control) => <Input {...control} value={p.classCode} onChange={(e) => updatePart(i, { classCode: e.target.value })} invalid={Boolean(f.classCode)} />}
                  </FormField>
                  {values.kind === "transfer" ? (
                    <FormField id={`part-${i}-legalEntityId`} label="Target legal entity" hint="Optional; leave empty to stay in the asset's legal entity." error={f.legalEntityId}>
                      {(control) => <Input {...control} value={p.legalEntityId} onChange={(e) => updatePart(i, { legalEntityId: e.target.value })} invalid={Boolean(f.legalEntityId)} />}
                    </FormField>
                  ) : null}
                </div>
                {values.kind === "split" && values.parts.length > 2 ? (
                  <div>
                    <Button size="sm" variant="ghost" onClick={() => update({ parts: values.parts.filter((_, j) => j !== i) })}>
                      {`Remove new asset ${i + 1}`}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            {values.kind === "split" ? (
              <Button size="sm" onClick={() => update({ parts: [...values.parts, blankPart()] })} disabled={values.parts.length >= MAX_PARTS}>
                Add new asset
              </Button>
            ) : null}
            {errors.parts ? (
              <span className={styles.error} role="alert">
                {errors.parts}
              </span>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0006" ? "The asset changed since you opened it" : "Could not record the transfer"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={blocked}>
              {values.kind === "split" ? "Split asset" : "Transfer asset"}
            </Button>
            <Link className={buttonClassName("ghost")} to={`/assets/${id}`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

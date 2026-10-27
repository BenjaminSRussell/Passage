import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getReceipt,
  listReceipts,
  newKey,
  receivePallet,
  RECEIPT_STATUSES,
  registerAsn,
  type AsnInput,
  type Problem,
  type Receipt,
  type ReceiptLine,
  type ReceiptStatus,
  type ReceiptSummary,
  type Received,
} from "./api/client";
import styles from "./pages.module.css";
import { ChooseWarehouse, DECIMAL, Detail, sentence, shortId, useWarehouse, UUID, WarehouseField, when, WORK_STATUS_TONES } from "./shared";
import { useLoad } from "./useLoad";

const PAGE = 100;
const LP_ID = /^[A-Za-z0-9._-]{1,40}$/;
const TOLERANCE = /^\d{1,2}(\.\d{1,2})?$/;

export const RECEIPT_STATUS_TONES: Record<ReceiptStatus, BadgeTone> = { OPEN: "neutral", PARTIALLY_RECEIVED: "info", RECEIVED: "success" };

function problemText(p: Problem): string {
  const fields = Object.entries(p.fields);
  return `${p.message} (${p.code})${fields.length > 0 ? `: ${fields.map(([f, m]) => `${f} ${m}`).join("; ")}` : ""}`;
}

const receiptColumns: DataTableColumn<ReceiptSummary>[] = [
  { key: "asnRef", header: "ASN", mono: true },
  { key: "status", header: "Status", render: (r) => <Badge tone={RECEIPT_STATUS_TONES[r.status] ?? "neutral"}>{sentence(r.status)}</Badge> },
  { key: "pallets", header: "Pallets received", align: "end", render: (r) => `${r.received} of ${r.lines}` },
  {
    key: "discrepancies",
    header: "Discrepancies",
    align: "end",
    render: (r) => (r.discrepancies > 0 ? <Badge tone="warning">{r.discrepancies}</Badge> : "0"),
  },
  { key: "tolerancePct", header: "Tolerance", align: "end", render: (r) => `±${r.tolerancePct}%` },
  { key: "createdAt", header: "Registered", mono: true, render: (r) => when(r.createdAt) },
];

/** The receiving workspace (FR-31.9): ASN receipts of a warehouse with how many pallets arrived and how many differ from the ASN. */
export function ReceiptsPage() {
  const navigate = useNavigate();
  const [warehouse, setWarehouse] = useWarehouse();
  const [status, setStatus] = useState("");
  const valid = UUID.test(warehouse.trim());
  const state = useLoad(
    (signal) => (valid ? listReceipts({ warehouseId: warehouse.trim(), status, limit: PAGE }, signal) : Promise.resolve(null)),
    `${warehouse.trim()}|${status}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Receiving"
        description="Receipts registered from advance ship notices. Open a receipt to receive its pallets; each received pallet becomes put-away work."
        actions={
          <Link className={buttonClassName("primary")} to={`/warehouse/receipts/new?warehouse=${encodeURIComponent(warehouse.trim())}`}>
            Register ASN
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <WarehouseField value={warehouse} onChange={setWarehouse} />
        <FormField id="receipt-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {RECEIPT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {!valid ? (
        <ChooseWarehouse />
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the receipts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Receipts"
          columns={receiptColumns}
          rows={state.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No receipts", description: "Register an ASN to receive its pallets." }}
          onRowOpen={(r) => navigate(`/warehouse/receipts/${r.id}`)}
        />
      )}
      {state.data?.nextCursor ? <p className={styles.note}>Showing the first {PAGE} receipts; filter by status to narrow the list.</p> : null}
    </div>
  );
}

interface PalletDraft {
  itemId: string;
  lpId: string;
  poLineRef: string;
  expectedQty: string;
  weight: string;
  volume: string;
}

export interface AsnDraft {
  warehouseId: string;
  asnRef: string;
  tolerancePct: string;
  pallets: PalletDraft[];
}

type AsnErrors = Partial<
  Record<"warehouseId" | "asnRef" | "tolerancePct" | `pallets.${number}.${"itemId" | "lpId" | "poLineRef" | "expectedQty" | "weight" | "volume"}`, string>
>;

const blankPallet = (): PalletDraft => ({ itemId: "", lpId: "", poLineRef: "", expectedQty: "", weight: "", volume: "" });

export function validateAsnDraft(d: AsnDraft): AsnErrors {
  const errors: AsnErrors = {};
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, a UUID.";
  const ref = d.asnRef.trim();
  if (ref === "" || ref.length > 100) errors.asnRef = "Enter the ASN reference, at most 100 characters.";
  if (d.tolerancePct !== "" && (!TOLERANCE.test(d.tolerancePct) || Number(d.tolerancePct) > 20)) errors.tolerancePct = "Enter 0 to 20, at most 2 decimals.";
  const seen = new Set<string>();
  d.pallets.forEach((p, i) => {
    const item = p.itemId.trim();
    if (item === "" || item.length > 60) errors[`pallets.${i}.itemId`] = "Enter the item.";
    const lp = p.lpId.trim();
    if (!LP_ID.test(lp)) errors[`pallets.${i}.lpId`] = "Enter 1 to 40 letters, digits, '.', '_' or '-'.";
    else if (seen.has(lp)) errors[`pallets.${i}.lpId`] = "Another pallet uses this license plate.";
    seen.add(lp);
    if (p.poLineRef.trim().length > 100) errors[`pallets.${i}.poLineRef`] = "Enter at most 100 characters.";
    if (!DECIMAL.test(p.expectedQty) || Number(p.expectedQty) <= 0) errors[`pallets.${i}.expectedQty`] = "Enter a quantity above 0.";
    if (p.weight !== "" && !DECIMAL.test(p.weight)) errors[`pallets.${i}.weight`] = "Enter a weight of 0 or more.";
    if (p.volume !== "" && !DECIMAL.test(p.volume)) errors[`pallets.${i}.volume`] = "Enter a volume of 0 or more.";
  });
  return errors;
}

export function toAsnInput(d: AsnDraft): AsnInput {
  return {
    warehouseId: d.warehouseId.trim().toLowerCase(),
    asnRef: d.asnRef.trim(),
    ...(d.tolerancePct !== "" ? { tolerancePct: d.tolerancePct } : {}),
    lines: d.pallets.map((p, i) => ({
      lineNo: i + 1,
      itemId: p.itemId.trim(),
      lpId: p.lpId.trim(),
      ...(p.poLineRef.trim() !== "" ? { poLineRef: p.poLineRef.trim() } : {}),
      expectedQty: p.expectedQty,
      ...(p.weight !== "" ? { weight: p.weight } : {}),
      ...(p.volume !== "" ? { volume: p.volume } : {}),
    })),
  };
}

/** Registers an ASN packing structure (FR-31.9): one row per pallet with its license plate and expected quantity. */
export function NewReceiptPage() {
  const navigate = useNavigate();
  const [warehouse] = useWarehouse();
  const [draft, setDraft] = useState<AsnDraft>({ warehouseId: warehouse, asnRef: "", tolerancePct: "", pallets: [blankPallet()] });
  const [errors, setErrors] = useState<AsnErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const set = (patch: Partial<AsnDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  };
  const setPallet = (i: number, patch: Partial<PalletDraft>) => set({ pallets: draft.pallets.map((p, j) => (j === i ? { ...p, ...patch } : p)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAsnDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const receipt = await registerAsn(toAsnInput(draft), key.current);
      navigate(`/warehouse/receipts/${receipt.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Register ASN" description="Enter the advance ship notice: one row per pallet with its license plate. Nothing is put away until you receive a pallet." />
      <Card title="ASN">
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <WarehouseField id="asn-warehouse" value={draft.warehouseId} onChange={(v) => set({ warehouseId: v })} error={errors.warehouseId} />
          </div>
          <FormField id="asn-ref" label="ASN reference" hint="The supplier's ASN number; one receipt per ASN and warehouse." error={errors.asnRef}>
            {(c) => <Input {...c} value={draft.asnRef} onChange={(e) => set({ asnRef: e.target.value })} invalid={Boolean(errors.asnRef)} />}
          </FormField>
          <FormField id="asn-tolerance" label="Variance tolerance (%)" optional hint="A pallet further than this from its ASN quantity is a discrepancy; 2 when blank." error={errors.tolerancePct}>
            {(c) => <Input {...c} inputMode="decimal" value={draft.tolerancePct} onChange={(e) => set({ tolerancePct: e.target.value })} invalid={Boolean(errors.tolerancePct)} />}
          </FormField>
          {draft.pallets.map((pallet, i) => {
            const field = (name: keyof PalletDraft, label: string, optional = false, decimal = false) => (
              <FormField id={`asn-${i}-${name}`} label={label} optional={optional} error={errors[`pallets.${i}.${name}`]}>
                {(c) => (
                  <Input
                    {...c}
                    inputMode={decimal ? "decimal" : undefined}
                    className={name === "lpId" ? styles.mono : undefined}
                    value={pallet[name]}
                    onChange={(e) => setPallet(i, { [name]: e.target.value })}
                    invalid={Boolean(errors[`pallets.${i}.${name}`])}
                  />
                )}
              </FormField>
            );
            return (
              <fieldset key={i} className={styles.lineRow} aria-label={`Pallet ${i + 1}`}>
                {field("itemId", `Pallet ${i + 1} item`)}
                {field("lpId", `Pallet ${i + 1} license plate`)}
                {field("expectedQty", `Pallet ${i + 1} expected quantity`, false, true)}
                {field("weight", `Pallet ${i + 1} weight (kg)`, true, true)}
                {field("volume", `Pallet ${i + 1} volume (m³)`, true, true)}
                {field("poLineRef", `Pallet ${i + 1} PO line`, true)}
                {draft.pallets.length > 1 ? (
                  <Button variant="ghost" size="sm" onClick={() => set({ pallets: draft.pallets.filter((_, j) => j !== i) })}>
                    Remove pallet {i + 1}
                  </Button>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button onClick={() => set({ pallets: [...draft.pallets, blankPallet()] })} disabled={draft.pallets.length >= 500}>
              Add pallet
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Register ASN
            </Button>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The ASN was not registered" : undefined} live="alert">
              {failure ? problemText(failure) : null}
            </Alert>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One receipt: receive each pallet with the quantity that arrived (FR-31.9); a variance beyond tolerance is recorded as a discrepancy (BR-31.10). */
export function ReceiptDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getReceipt(id, signal), id);
  const [current, setCurrent] = useState<Receipt | null>(null);
  const [receiving, setReceiving] = useState<{ lineNo: number; qty: string } | null>(null);
  const [qtyError, setQtyError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<Received | null>(null);
  const [busy, setBusy] = useState(false);
  const keys = useRef<Record<string, string>>({});
  useEffect(() => {
    setCurrent(null);
    setReceiving(null);
    setResult(null);
    setFailure(null);
  }, [id]);

  const receipt = current ?? state.data;
  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Receipt" />
        <Alert tone="danger" title="Could not load the receipt" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!receipt) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Receipt" />
        <span className="visually-hidden">Loading the receipt…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const loaded = receipt;

  function start(line: ReceiptLine) {
    setReceiving({ lineNo: line.lineNo, qty: line.expectedQty });
    setQtyError(undefined);
    setFailure(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!receiving) return;
    if (!DECIMAL.test(receiving.qty) || Number(receiving.qty) <= 0) {
      setQtyError("Enter the quantity that arrived, above 0.");
      return;
    }
    setQtyError(undefined);
    setFailure(null);
    setBusy(true);
    const action = `${receiving.lineNo}|${receiving.qty}`;
    try {
      const out = await receivePallet(loaded, receiving.lineNo, receiving.qty, (keys.current[action] ??= newKey()));
      delete keys.current[action];
      setCurrent(out.receipt);
      setResult(out);
      setReceiving(null);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "WMS-VERSION-CONFLICT") {
        // Someone else received a pallet meanwhile: show the current receipt, then retry.
        setCurrent(null);
        state.reload();
      }
    } finally {
      setBusy(false);
    }
  }

  const discrepancies = loaded.lines.filter((l) => l.discrepancy).length;
  const columns: DataTableColumn<ReceiptLine>[] = [
    { key: "lineNo", header: "Pallet", align: "end" },
    { key: "lpId", header: "License plate", mono: true },
    { key: "itemId", header: "Item", mono: true },
    { key: "expectedQty", header: "ASN quantity", align: "end" },
    { key: "receivedQty", header: "Received", align: "end", render: (l) => (l.status === "RECEIVED" ? l.receivedQty : "—") },
    {
      key: "variance",
      header: "Variance",
      align: "end",
      render: (l) =>
        l.status !== "RECEIVED" ? (
          "—"
        ) : (
          <span>
            {l.varianceQty} ({Number(l.variancePct).toFixed(2)}%){" "}
            {l.discrepancy ? <Badge tone="warning">Discrepancy</Badge> : null}
          </span>
        ),
    },
    {
      key: "status",
      header: "Status",
      render: (l) => <Badge tone={l.status === "RECEIVED" ? "success" : "neutral"}>{sentence(l.status)}</Badge>,
    },
    {
      key: "work",
      header: "Put-away work",
      mono: true,
      render: (l) => (l.workId ? <Link to={`/warehouse/work/${l.workId}`}>{shortId(l.workId)}</Link> : "—"),
    },
    {
      key: "actions",
      header: "Actions",
      render: (l) =>
        l.status === "EXPECTED" ? (
          <Button size="sm" variant="primary" onClick={() => start(l)} aria-label={`Receive pallet ${l.lineNo}`}>
            Receive
          </Button>
        ) : (
          "—"
        ),
    },
  ];
  const pallet = receiving ? loaded.lines.find((l) => l.lineNo === receiving.lineNo) : undefined;

  return (
    <div className={styles.stack}>
      <PageHeader title={`ASN ${loaded.asnRef}`} meta={<Badge tone={RECEIPT_STATUS_TONES[loaded.status] ?? "neutral"}>{sentence(loaded.status)}</Badge>} />
      <Card>
        <dl className={styles.details}>
          <Detail term="Receipt id" mono>
            {loaded.id}
          </Detail>
          <Detail term="Warehouse" mono>
            {shortId(loaded.warehouseId)}
          </Detail>
          <Detail term="Variance tolerance">±{loaded.tolerancePct}%</Detail>
          <Detail term="Pallets received">
            {loaded.lines.filter((l) => l.status === "RECEIVED").length} of {loaded.lines.length}
          </Detail>
          <Detail term="Discrepancies">{discrepancies}</Detail>
          <Detail term="Registered">{when(loaded.createdAt)}</Detail>
          <Detail term="Version">{loaded.version}</Detail>
        </dl>
      </Card>
      <Alert tone="danger" title={failure ? "The pallet was not received" : undefined} live="alert">
        {failure ? problemText(failure) : null}
      </Alert>
      <Alert tone={result && result.warnings.length > 0 ? "warning" : "success"} title={result ? "Pallet received" : undefined} live="status">
        {result ? <ReceivedNotice result={result} /> : null}
      </Alert>
      <DataTable label="Pallets" columns={columns} rows={loaded.lines} getRowKey={(l) => String(l.lineNo)} />
      {receiving && pallet ? (
        <Card title={`Receive pallet ${pallet.lineNo} (${pallet.lpId})`}>
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField
              id="receive-qty"
              label="Quantity received"
              hint={`The ASN says ${pallet.expectedQty} ${pallet.itemId}. Enter what actually arrived; it is received either way.`}
              error={qtyError}
            >
              {(c) => (
                <Input {...c} inputMode="decimal" value={receiving.qty} onChange={(e) => setReceiving({ ...receiving, qty: e.target.value })} invalid={Boolean(qtyError)} />
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Receive pallet
              </Button>
              <Button variant="ghost" onClick={() => setReceiving(null)}>
                Not now
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}

function ReceivedNotice({ result }: { result: Received }) {
  const blocked = result.work.status === "BLOCKED_NO_LOCATION";
  return (
    <span className={styles.summary}>
      {result.warnings.map((w) => (
        <span key={w.code}>{w.detail}</span>
      ))}
      <span>
        Put-away work <Link to={`/warehouse/work/${result.work.id}`}>{shortId(result.work.id)}</Link>{" "}
        <Badge tone={WORK_STATUS_TONES[result.work.status] ?? "neutral"}>{sentence(result.work.status)}</Badge>
        {blocked ? " — no directive could place the pallet (WMS-0101)." : "."}
      </span>
    </span>
  );
}


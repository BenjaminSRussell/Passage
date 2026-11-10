import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  advanceBatchOrder,
  ALLOCATION_METHODS,
  BY_PRODUCT_COST_TYPES,
  describeProblem,
  endBatchOrder,
  newKey,
  ProcessApiError,
  reportBatchOrder,
  type AllocationMethod,
  type AllocationShare,
  type BatchOrder,
  type ByProductCostType,
  type EndInput,
  type OrderLine,
  type Problem,
} from "./api/client";
import { PERCENT, QUANTITY, shortId } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const METHOD_LABELS: Record<AllocationMethod, string> = {
  tca: "Total cost allocation (standard cost x quantity)",
  quantity: "By reported quantity",
  manual: "Manual percentages",
  none: "All cost on the main item",
};

const COST_TYPE_LABELS: Record<ByProductCostType, string> = {
  "per-quantity": "Per unit of quantity",
  "per-series": "Per unit of quantity (series)",
  percent: "Percent of batch cost",
  recycled: "Per unit, recycled",
};

const ROLE_LABELS: Record<AllocationShare["role"], string> = {
  main: "Main item",
  "co-product": "Co-product",
  "by-product-burden": "By-product burden",
};

const COST = /^\d{1,14}(\.\d{1,4})?$/;

function isPositive(value: string): boolean {
  return QUANTITY.test(value) && !/^0+(\.0+)?$/.test(value);
}

/**
 * Runs one lifecycle command. The key is kept only when the outcome is unknown
 * (network failure or a 5xx), so a retry replays the first answer; after any
 * definite answer, or an edit, the next attempt gets a fresh key.
 */
function useRunner(onChanged: (order: BatchOrder) => void) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());
  async function run(command: (key: string) => Promise<BatchOrder>) {
    setFailure(null);
    setBusy(true);
    try {
      const updated = await command(key.current);
      key.current = newKey();
      onChanged(updated);
    } catch (error) {
      setFailure(describeProblem(error));
      if (error instanceof ProcessApiError && error.status >= 400 && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }
  return { busy, failure, run, renew: () => (key.current = newKey()) };
}

function Failure({ title, failure, onReload }: { title: string; failure: Problem | null; onReload?: () => void }) {
  if (!failure) return null;
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        failure.code === "PMF-0104" || failure.code === "PMF-0106" ? (
          <Button size="sm" onClick={onReload}>
            Reload order
          </Button>
        ) : undefined
      }
    >
      <ProblemMessage problem={failure} />
      {Object.keys(failure.fields).length > 0 ? (
        <ul className={styles.fieldErrors}>
          {Object.entries(failure.fields).map(([field, message]) => (
            <li key={field}>
              <code>{field}</code> {message}
            </li>
          ))}
        </ul>
      ) : null}
    </Alert>
  );
}

const shareColumns: DataTableColumn<AllocationShare>[] = [
  { key: "role", header: "Recipient", render: (s) => ROLE_LABELS[s.role] },
  { key: "lineNumber", header: "Line", align: "end", render: (s) => (s.role === "main" ? "main" : String(s.lineNumber)) },
  { key: "itemId", header: "Item", mono: true, render: (s) => shortId(s.itemId) },
  { key: "rafQty", header: "Reported qty", align: "end" },
  { key: "amount", header: "Cost", align: "end" },
  { key: "percent", header: "Share %", align: "end", render: (s) => (s.role === "by-product-burden" ? "—" : s.percent) },
];

/** The cost split an ended order carries (BR-24.3). */
export function AllocationCard({ order }: { order: BatchOrder }) {
  const a = order.allocation;
  if (!a) return null;
  return (
    <Card title="Cost allocation">
      <div className={styles.stack}>
        <DataTable
          label="Cost allocation"
          columns={shareColumns}
          rows={a.shares}
          getRowKey={(s) => `${s.role}-${s.lineNumber}`}
          empty={{ title: "No shares", description: "The order ended without an allocation." }}
        />
        <p className={styles.note}>{`${METHOD_LABELS[a.method]}. The shares add up to the batch cost of ${a.batchCost} ${a.currency}.`}</p>
      </div>
    </Card>
  );
}

function ReportForm({ order, onChanged, reload }: { order: BatchOrder; onChanged(o: BatchOrder): void; reload(): void }) {
  const outputs = (order.lines ?? []).filter((l) => l.type === "co-product" || l.type === "by-product");
  const [mainQty, setMainQty] = useState(order.batchSize);
  const [qty, setQty] = useState<Record<number, string>>(() => Object.fromEntries(outputs.map((l) => [l.lineNumber, l.qty])));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const runner = useRunner(onChanged);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!isPositive(mainQty)) found.main = "Enter a positive quantity with at most 6 decimals.";
    for (const l of outputs) {
      if (!QUANTITY.test(qty[l.lineNumber] ?? "")) found[`line${l.lineNumber}`] = "Enter a quantity, zero or more, with at most 6 decimals.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void runner.run((key) =>
      reportBatchOrder(order.id, { version: order.version, mainQty, outputs: outputs.map((l) => ({ lineNumber: l.lineNumber, qty: qty[l.lineNumber] })) }, key),
    );
  }

  return (
    <Card title="Report as finished">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="raf-main" label={`Finished quantity (${order.unit})`} error={errors.main}>
          {(c) => (
            <Input
              {...c}
              inputMode="decimal"
              value={mainQty}
              onChange={(e) => {
                setMainQty(e.target.value);
                runner.renew();
              }}
              invalid={Boolean(errors.main)}
            />
          )}
        </FormField>
        {outputs.map((l: OrderLine) => (
          <FormField key={l.lineNumber} id={`raf-line-${l.lineNumber}`} label={`Line ${l.lineNumber} ${l.type} (${shortId(l.itemId)}), planned ${l.qty}`} error={errors[`line${l.lineNumber}`]}>
            {(c) => (
              <Input
                {...c}
                inputMode="decimal"
                value={qty[l.lineNumber] ?? ""}
                onChange={(e) => {
                  setQty({ ...qty, [l.lineNumber]: e.target.value });
                  runner.renew();
                }}
                invalid={Boolean(errors[`line${l.lineNumber}`])}
              />
            )}
          </FormField>
        ))}
        <Failure title="Could not report the batch order" failure={runner.failure} onReload={reload} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={runner.busy}>
            Report as finished
          </Button>
        </div>
      </form>
    </Card>
  );
}

function EndForm({ order, onChanged, reload }: { order: BatchOrder; onChanged(o: BatchOrder): void; reload(): void }) {
  const coLines = (order.lines ?? []).filter((l) => l.type === "co-product");
  const byLines = (order.lines ?? []).filter((l) => l.type === "by-product");
  const [method, setMethod] = useState<AllocationMethod>("tca");
  const [currency, setCurrency] = useState("USD");
  const [batchCost, setBatchCost] = useState("");
  const [main, setMain] = useState("");
  const [co, setCo] = useState<Record<number, string>>({});
  const [by, setBy] = useState<Record<number, { costType: ByProductCostType; rate: string }>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const runner = useRunner(onChanged);
  const weighted = method === "tca" || method === "manual";
  const weightLabel = method === "tca" ? "Standard cost per unit" : "Percent";
  const weightOk = (v: string) => (method === "tca" ? QUANTITY.test(v) : PERCENT.test(v));

  const edit = <T,>(set: (v: T) => void) => (v: T) => {
    set(v);
    runner.renew();
  };

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!/^[A-Z]{3}$/.test(currency)) found.currency = "Enter an ISO 4217 code such as USD.";
    if (!COST.test(batchCost)) found.batchCost = "Enter the batch cost, zero or more, with at most 4 decimals.";
    if (weighted) {
      if (!weightOk(main)) found.main = `Enter the ${weightLabel.toLowerCase()} of the main item.`;
      for (const l of coLines) if (!weightOk(co[l.lineNumber] ?? "")) found[`co${l.lineNumber}`] = `Enter the ${weightLabel.toLowerCase()}.`;
    }
    for (const l of byLines) {
      const rate = by[l.lineNumber]?.rate ?? "";
      if (rate !== "" && !QUANTITY.test(rate)) found[`by${l.lineNumber}`] = "Enter a rate with at most 6 decimals, or leave it empty for no burden.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const field = method === "tca" ? "standardCost" : "percent";
    const input: EndInput = {
      version: order.version,
      method,
      currency,
      batchCost,
      main: weighted ? { [field]: main } : {},
      coProducts: weighted ? coLines.map((l) => ({ lineNumber: l.lineNumber, [field]: co[l.lineNumber] })) : [],
      byProducts: byLines
        .filter((l) => (by[l.lineNumber]?.rate ?? "") !== "")
        .map((l) => ({ lineNumber: l.lineNumber, costType: by[l.lineNumber].costType, rate: by[l.lineNumber].rate })),
    };
    void runner.run((key) => endBatchOrder(order.id, input, key));
  }

  return (
    <Card title="End and allocate cost">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="end-method" label="Allocation method">
          {(c) => (
            <Select {...c} value={method} onChange={(e) => edit(setMethod)(e.target.value as AllocationMethod)}>
              {ALLOCATION_METHODS.map((m) => (
                <option key={m} value={m}>
                  {METHOD_LABELS[m]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="end-currency" label="Currency" error={errors.currency}>
          {(c) => <Input {...c} value={currency} onChange={(e) => edit(setCurrency)(e.target.value.toUpperCase())} invalid={Boolean(errors.currency)} />}
        </FormField>
        <FormField id="end-cost" label="Batch cost" error={errors.batchCost}>
          {(c) => <Input {...c} inputMode="decimal" value={batchCost} onChange={(e) => edit(setBatchCost)(e.target.value)} invalid={Boolean(errors.batchCost)} />}
        </FormField>
        {weighted ? (
          <>
            <FormField id="end-main" label={`Main item: ${weightLabel.toLowerCase()}`} error={errors.main}>
              {(c) => <Input {...c} inputMode="decimal" value={main} onChange={(e) => edit(setMain)(e.target.value)} invalid={Boolean(errors.main)} />}
            </FormField>
            {coLines.map((l) => (
              <FormField key={l.lineNumber} id={`end-co-${l.lineNumber}`} label={`Co-product line ${l.lineNumber}: ${weightLabel.toLowerCase()}`} error={errors[`co${l.lineNumber}`]}>
                {(c) => (
                  <Input
                    {...c}
                    inputMode="decimal"
                    value={co[l.lineNumber] ?? ""}
                    onChange={(e) => edit(setCo)({ ...co, [l.lineNumber]: e.target.value })}
                    invalid={Boolean(errors[`co${l.lineNumber}`])}
                  />
                )}
              </FormField>
            ))}
          </>
        ) : null}
        {byLines.map((l) => (
          <div key={l.lineNumber} className={styles.toolbar}>
            <FormField id={`end-by-type-${l.lineNumber}`} label={`By-product line ${l.lineNumber}: cost type`}>
              {(c) => (
                <Select
                  {...c}
                  value={by[l.lineNumber]?.costType ?? "per-quantity"}
                  onChange={(e) => edit(setBy)({ ...by, [l.lineNumber]: { rate: by[l.lineNumber]?.rate ?? "", costType: e.target.value as ByProductCostType } })}
                >
                  {BY_PRODUCT_COST_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {COST_TYPE_LABELS[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`end-by-rate-${l.lineNumber}`} label={`By-product line ${l.lineNumber}: rate`} error={errors[`by${l.lineNumber}`]} hint="Leave empty for no burden.">
              {(c) => (
                <Input
                  {...c}
                  inputMode="decimal"
                  value={by[l.lineNumber]?.rate ?? ""}
                  onChange={(e) => edit(setBy)({ ...by, [l.lineNumber]: { costType: by[l.lineNumber]?.costType ?? "per-quantity", rate: e.target.value } })}
                  invalid={Boolean(errors[`by${l.lineNumber}`])}
                />
              )}
            </FormField>
          </div>
        ))}
        <Failure title="Could not end the batch order" failure={runner.failure} onReload={reload} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={runner.busy}>
            End order and allocate cost
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Release, start, report and end controls for the order's current status. */
export function OrderLifecycle({ order, onChanged, reload }: { order: BatchOrder; onChanged(o: BatchOrder): void; reload(): void }) {
  const runner = useRunner(onChanged);
  if (order.status === "planned" || order.status === "released") {
    const command = order.status === "planned" ? "release" : "start";
    return (
      <Card title="Lifecycle">
        <div className={styles.stack}>
          <p className={styles.note}>
            {order.status === "planned"
              ? "The order is planned. Release it to make it available to the shop floor."
              : "The order is released. Starting it locks the batch size and the formula snapshot."}
          </p>
          <Failure title={`Could not ${command} the batch order`} failure={runner.failure} onReload={reload} />
          <div className={styles.formActions}>
            <Button variant="primary" loading={runner.busy} onClick={() => void runner.run((key) => advanceBatchOrder(order.id, command, order.version, key))}>
              {command === "release" ? "Release order" : "Start order"}
            </Button>
          </div>
        </div>
      </Card>
    );
  }
  if (order.status === "started") return <ReportForm key={order.version} order={order} onChanged={onChanged} reload={reload} />;
  if (order.status === "reported") return <EndForm key={order.version} order={order} onChanged={onChanged} reload={reload} />;
  return null;
}

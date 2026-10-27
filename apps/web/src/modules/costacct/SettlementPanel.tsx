import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import { describeError, listCostElements, listCostObjects, listSettlements, settleInternalOrder, type CostEntry, type CostObject, type Settlement } from "./api/client";
import { formatAmount, formatTicks, settlementError, sumTicks, UUID } from "./logic";
import styles from "./pages.module.css";
import { formatDateTime, LoadError } from "./parts";
import { useLoad } from "./useLoad";

/**
 * Internal order settlement for one period (FR-38.9): collected cost per currency, the settlement
 * form and the settlement history. A settlement cannot exceed the collected cost (BR-38.9).
 */
export function SettlementPanel({ order, period, entries, onSettled }: { order: CostObject; period: string; entries: CostEntry[]; onSettled(): void }) {
  const [reload, setReload] = useState(0);
  const history = useLoad((signal) => listSettlements(order.id, period, signal), `${order.id}|${period}|${reload}`);
  const objects = useLoad((signal) => listCostObjects({ legalEntityId: order.legalEntityId }, signal), order.legalEntityId);
  const elements = useLoad((signal) => listCostElements(order.legalEntityId, signal), order.legalEntityId);
  const currencies = [...new Set(entries.map((e) => e.currency))];
  const [form, setForm] = useState({ currency: "", receiverCostObjectId: "", secondaryElementId: "", amount: "" });
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<Settlement | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  const currency = form.currency || currencies[0] || "";
  const collected = sumTicks(entries.filter((e) => e.currency === currency).map((e) => e.amount));
  const receivers = (objects.data?.items ?? []).filter((o) => o.id !== order.id);
  const secondaries = (elements.data?.items ?? []).filter((e) => e.kind === "Secondary");
  const code = (id: string) => (objects.data?.items ?? []).find((o) => o.id === id)?.code ?? id;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const input = { period, currency, receiverCostObjectId: form.receiverCostObjectId, secondaryElementId: form.secondaryElementId, amount: form.amount.trim() };
    const found: string[] = [];
    if (!currency) found.push("Nothing is collected on the order in this period.");
    if (!UUID.test(input.receiverCostObjectId)) found.push("Choose the receiver of the settlement.");
    if (!UUID.test(input.secondaryElementId)) found.push("Choose the secondary element that carries the settlement.");
    const amountError = settlementError(input.amount, collected);
    if (amountError) found.push(amountError);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (found.length > 0) return;
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      setDone(await settleInternalOrder(order.id, input, key.current.value));
      key.current = { body: "", value: crypto.randomUUID() };
      setForm((f) => ({ ...f, amount: "" }));
      setReload((n) => n + 1);
      onSettled();
    } catch (err) {
      setFailure(describeError(err, "The settlement could not be posted."));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<Settlement>[] = [
    { key: "createdAt", header: "Settled", render: (s) => formatDateTime(s.createdAt) },
    { key: "receiverCostObjectId", header: "Receiver", mono: true, render: (s) => code(s.receiverCostObjectId) },
    { key: "amount", header: "Amount", align: "end", mono: true, render: (s) => formatAmount(s.amount) },
    { key: "remaining", header: "Left on the order", align: "end", mono: true, render: (s) => formatAmount(s.remaining) },
    { key: "currency", header: "Currency", mono: true },
  ];

  return (
    <>
      <Card title="Settle the internal order" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>
            Collected in {period}: {collected === null || !currency ? "nothing" : `${formatAmount(formatTicks(collected))} ${currency}`}. The order is credited and the
            receiver debited on the settlement element.
          </p>
          {currencies.length > 1 ? (
            <FormField id="settle-currency" label="Currency">
              {(c) => (
                <Select {...c} value={currency} onChange={(e) => setForm((f) => ({ ...f, currency: e.target.value }))}>
                  {currencies.map((cur) => (
                    <option key={cur} value={cur}>
                      {cur}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          <FormField id="settle-receiver" label="Receiver">
            {(c) => (
              <Select {...c} value={form.receiverCostObjectId} onChange={(e) => setForm((f) => ({ ...f, receiverCostObjectId: e.target.value }))}>
                <option value="">Choose a receiver</option>
                {receivers.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.code} {o.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="settle-element" label="Settlement element">
            {(c) => (
              <Select {...c} value={form.secondaryElementId} onChange={(e) => setForm((f) => ({ ...f, secondaryElementId: e.target.value }))}>
                <option value="">Choose a secondary element</option>
                {secondaries.map((el) => (
                  <option key={el.id} value={el.id}>
                    {el.code} {el.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="settle-amount" label="Settlement amount">
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={form.amount} onChange={(e) => setForm((f) => ({ ...f, amount: e.target.value }))} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Settle
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {errors.length > 0 ? (
          <ul className={styles.errorList}>
            {errors.map((msg) => (
              <li key={msg}>{msg}</li>
            ))}
          </ul>
        ) : (
          failure
        )}
      </Alert>
      <Alert tone="success" live="status">
        {done ? `Settled ${formatAmount(done.amount)} ${done.currency} to ${code(done.receiverCostObjectId)}; ${formatAmount(done.remaining)} is left on the order.` : null}
      </Alert>
      {history.error ? (
        <LoadError title="Could not load settlements" error={history.error} onRetry={history.reload} />
      ) : (
        <DataTable
          label="Settlements"
          columns={columns}
          rows={history.data?.items ?? []}
          getRowKey={(s) => s.id}
          loading={history.loading && !history.data}
          empty={{ title: "No settlements in this period", description: "Settled amounts appear here with what was left on the order." }}
        />
      )}
    </>
  );
}

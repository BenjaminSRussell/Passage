import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listContainerTypes, listReturnableBalances, returnPackaging, type ContainerType, type Problem, type ReturnableBalance } from "./api/client";
import { formatDateTime, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ReturnableBalance>[] = [
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "containerType", header: "Container type", mono: true },
  { key: "balance", header: "Held by customer", align: "end" },
  { key: "lastShippedAt", header: "Last shipped", render: (row) => formatDateTime(row.lastShippedAt) },
  { key: "lastReturnedAt", header: "Last returned", render: (row) => formatDateTime(row.lastReturnedAt) },
];

interface Data {
  balances: ReturnableBalance[];
  types: ContainerType[];
}

interface Values {
  customerId: string;
  containerType: string;
  qty: string;
}

const blank: Values = { customerId: "", containerType: "", qty: "" };

/**
 * Returnable transport packaging (FR-28.16): pallets and crates each customer
 * holds. Posting a packing slip adds its returnable containers; a return
 * receipt takes them off, never below zero (FUL-0015).
 */
export function ReturnablePackagingPage() {
  const [customer, setCustomer] = useState("");
  const filter = UUID.test(customer.trim()) ? customer.trim() : "";
  const state = useLoad<Data>(async (signal) => {
    const [balances, types] = await Promise.all([listReturnableBalances({ customerId: filter }, signal), listContainerTypes(signal)]);
    return { balances, types: types.filter((t) => t.returnable) };
  }, filter);
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(values.customerId.trim())) found.customerId = "Enter the customer id (a UUID).";
    if (values.containerType === "") found.containerType = "Choose the returnable container type.";
    if (!/^[1-9]\d{0,8}$/.test(values.qty.trim())) found.qty = "Enter how many containers came back, a whole number of 1 or more.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await returnPackaging({ customerId: values.customerId.trim(), containerType: values.containerType, qty: values.qty.trim() }, key.current());
      key.renew();
      setValues(blank);
      setNotice(`Received. Customer ${shortId(saved.customerId)} now holds ${saved.balance} ${saved.containerType}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Returnable packaging"
        description="Returnable pallets and crates each customer holds. Posting a packing slip adds its returnable containers; a return receipt takes them off."
      />
      <div className={styles.toolbar}>
        <FormField id="rtp-filter" label="Customer id" hint="Show one customer's balances." optional>
          {(control) => <Input {...control} value={customer} onChange={(event) => setCustomer(event.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load returnable balances" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Returnable packaging balances"
          columns={columns}
          rows={state.data?.balances ?? []}
          getRowKey={(row) => `${row.customerId}-${row.containerType}`}
          loading={state.loading}
          onRowOpen={(row) => {
            setValues({ customerId: row.customerId, containerType: row.containerType, qty: "" });
            key.renew();
          }}
          empty={
            filter
              ? { title: "This customer holds no returnable packaging", description: "Balances appear when a packing slip ships returnable containers to the customer." }
              : { title: "No returnable packaging out", description: "Mark container types as returnable; shipping them builds the customer balances." }
          }
        />
      )}
      <Card title="Receive returned packaging">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>A return above the customer&apos;s balance is refused (FUL-0015). Open a row above to fill in its customer and type.</p>
          <FormField id="rtp-customer" label="Customer id" error={errors.customerId}>
            {(control) => <Input {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => set({ customerId: event.target.value })} />}
          </FormField>
          <FormField id="rtp-type" label="Container type" error={errors.containerType}>
            {(control) => (
              <Select {...control} value={values.containerType} onChange={(event) => set({ containerType: event.target.value })}>
                <option value="">Choose a returnable type</option>
                {(state.data?.types ?? []).map((t) => (
                  <option key={t.code} value={t.code}>
                    {t.code}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rtp-qty" label="Quantity returned" error={errors.qty}>
            {(control) => <Input {...control} inputMode="numeric" value={values.qty} invalid={Boolean(errors.qty)} onChange={(event) => set({ qty: event.target.value })} />}
          </FormField>
          <ProblemAlert title="Could not record the return" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record return
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}

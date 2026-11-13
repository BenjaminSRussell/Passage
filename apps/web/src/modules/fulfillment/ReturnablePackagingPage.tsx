import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Button, buttonClassName, Card, FormField, Input, ListPage, loadedRows, Select, type DataTableColumn, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, listContainerTypes, listReturnableBalances, returnPackaging, type Problem, type ReturnableBalance } from "./api/client";
import { formatDateTime, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { PlainName } from "../sales/OrderNumber";

const columns: DataTableColumn<ReturnableBalance>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName short entity="customer" id={row.customerId} /> },
  { key: "containerType", header: "Container type", mono: true },
  { key: "balance", header: "Held by customer", align: "end" },
  { key: "lastShippedAt", header: "Last shipped", render: (row) => formatDateTime(row.lastShippedAt) },
  { key: "lastReturnedAt", header: "Last returned", render: (row) => formatDateTime(row.lastReturnedAt) },
];


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
const balanceFilters: ListFilterDef[] = [{ kind: "entity", key: "customerId", label: "Customer", entity: "customer" }];

export function ReturnablePackagingPage() {
  // Bumped after a return, so the list reads the new balance.
  const [reloads, setReloads] = useState(0);
  const typeState = useLoad((signal) => listContainerTypes(signal).then((types) => types.filter((t) => t.returnable)), "returnable-types");
  const useBalanceRows = (query: ListQuery) => {
    const filter = query.filters.customerId ?? "";
    return loadedRows(useLoad((signal) => listReturnableBalances({ customerId: filter }, signal), `${filter}-${reloads}`));
  };
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
      setReloads((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <ListPage
      id="fulfillment-returnable-packaging"
      title="Returnable packaging"
      description="Returnable pallets and crates each customer holds. Posting a packing slip adds its returnable containers; a return receipt takes them off."
      actions={
        <Link className={buttonClassName("secondary")} to="/fulfillment/returnable-movements">
          Movements and corrections
        </Link>
      }
      noun="balances"
      singular="balance"
      columns={columns}
      filters={balanceFilters}
      useRows={useBalanceRows}
      getRowKey={(row) => `${row.customerId}-${row.containerType}`}
      onRowOpen={(row) => {
        setValues({ customerId: row.customerId, containerType: row.containerType, qty: "" });
        key.renew();
      }}
      empty={{ title: "No returnable packaging out", description: "Mark container types as returnable; shipping them builds the customer balances." }}
      footer={
        <>
      <Card title="Receive returned packaging">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>A return above the customer&apos;s balance is refused (FUL-0015). Open a row above to fill in its customer and type.</p>
          <FormField id="rtp-customer" label="Customer" error={errors.customerId}>
            {(control) => <EntityLookup entity="customer" {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => set({ customerId: event.target.value })} />}
          </FormField>
          <FormField id="rtp-type" label="Container type" error={errors.containerType}>
            {(control) => (
              <Select {...control} value={values.containerType} onChange={(event) => set({ containerType: event.target.value })}>
                <option value="">Choose a returnable type</option>
                {(typeState.data ?? []).map((t) => (
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
        </>
      }
    />
  );
}

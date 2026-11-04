import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { fetchDimensionValues, fetchLedger, LedgerApiError, putDimensionValue, type DimensionValue, type DimensionValueInput } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import styles from "./pages.module.css";

const VALUE = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const MAX_NAME = 60;

export interface ValueDraft {
  attribute: string;
  value: string;
  name: string;
  activeFrom: string;
  activeTo: string;
  suspended: boolean;
}

type Errors = Record<string, string>;

export const emptyValue: ValueDraft = { attribute: "", value: "", name: "", activeFrom: "", activeTo: "", suspended: false };

/** Client checks that mirror PUT .../dimension-values/{attribute}/{value} (FR-11.2). */
export function validateValue(v: ValueDraft, attributes: string[]): Errors {
  const errors: Errors = {};
  if (!attributes.includes(v.attribute)) errors.attribute = "Pick one of the ledger's dimensions.";
  if (!VALUE.test(v.value)) errors.value = "Use 1 to 20 letters, digits, dots, dashes or underscores.";
  if (v.name.trim().length > MAX_NAME) errors.name = `Use at most ${MAX_NAME} characters.`;
  if (v.activeFrom !== "" && !DATE.test(v.activeFrom)) errors.activeFrom = "Enter a date.";
  if (v.activeTo !== "" && !DATE.test(v.activeTo)) errors.activeTo = "Enter a date.";
  else if (v.activeFrom !== "" && v.activeTo !== "" && v.activeTo < v.activeFrom) errors.activeTo = "The end date can't be before the start date.";
  return errors;
}

export function toValueInput(v: ValueDraft): DimensionValueInput {
  return { name: v.name.trim(), activeFrom: v.activeFrom || null, activeTo: v.activeTo || null, suspended: v.suspended };
}

/** Display status of a registered value on a given day (YYYY-MM-DD). */
export function valueStatus(v: DimensionValue, today: string): string {
  if (v.suspended) return "suspended";
  if (v.activeFrom && today < v.activeFrom) return "not yet active";
  if (v.activeTo && today > v.activeTo) return "expired";
  return "active";
}

function todayISO(): string {
  return new Date().toISOString().slice(0, 10);
}

function ValueForm({
  ledgerId,
  attributes,
  draft,
  onDraft,
  existing,
}: {
  ledgerId: string;
  attributes: string[];
  draft: ValueDraft;
  onDraft: (d: ValueDraft) => void;
  /** The registered values as last read: the matching one's lockVersion is sent as If-Match. */
  existing: DimensionValue[];
}) {
  const queryClient = useQueryClient();
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<ValueDraft>) => onDraft({ ...draft, ...patch });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateValue(draft, attributes);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const v = await putDimensionValue(
        ledgerId,
        draft.attribute,
        draft.value,
        toValueInput(draft),
        existing.find((x) => x.attribute === draft.attribute && x.value === draft.value)?.lockVersion,
      );
      await queryClient.invalidateQueries({ queryKey: ["ledger", "dimension-values", ledgerId] });
      setSaved(`Saved ${v.attribute} ${v.value}.`);
      onDraft({ ...emptyValue, attribute: draft.attribute });
    } catch (error) {
      setFailure(error instanceof LedgerApiError ? `${error.message} (${error.code})` : (error as Error).message);
      // A stale or missing tag means the list is out of date: reload it so the next save sends the current tag.
      void queryClient.invalidateQueries({ queryKey: ["ledger", "dimension-values", ledgerId] });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Add or change a dimension value">
        <FormField id="dv-attribute" label="Dimension" error={errors.attribute}>
          {(c) => (
            <Select {...c} value={draft.attribute} onChange={(e) => update({ attribute: e.target.value })} invalid={Boolean(errors.attribute)}>
              <option value="">Choose a dimension</option>
              {attributes.map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="dv-value" label="Value" hint="Saving an existing value replaces it." error={errors.value}>
          {(c) => <Input {...c} className={styles.mono} maxLength={20} value={draft.value} onChange={(e) => update({ value: e.target.value })} invalid={Boolean(errors.value)} />}
        </FormField>
        <FormField id="dv-name" label="Name" optional error={errors.name}>
          {(c) => <Input {...c} maxLength={MAX_NAME} value={draft.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="dv-from" label="Active from" optional error={errors.activeFrom}>
          {(c) => <Input {...c} type="date" value={draft.activeFrom} onChange={(e) => update({ activeFrom: e.target.value })} invalid={Boolean(errors.activeFrom)} />}
        </FormField>
        <FormField id="dv-to" label="Active to" optional hint="The last day postings may use the value." error={errors.activeTo}>
          {(c) => <Input {...c} type="date" value={draft.activeTo} onChange={(e) => update({ activeTo: e.target.value })} invalid={Boolean(errors.activeTo)} />}
        </FormField>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="dv-suspended" type="checkbox" checked={draft.suspended} onChange={(e) => update({ suspended: e.target.checked })} />
            <label htmlFor="dv-suspended">Suspended</label>
          </span>
          <span className={styles.note}>Postings with a suspended value, or outside its dates, are refused.</span>
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the dimension value" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" live="status">
              {saved}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save value
          </Button>
          <Button variant="ghost" onClick={() => onDraft({ ...emptyValue, attribute: draft.attribute })}>
            Clear
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Registered financial dimension values of a ledger with their dates and suspension (FR-11.2). */
export function DimensionValuesPage() {
  const [ledgerId, setLedgerId] = useLedgerParam();
  const [draft, setDraft] = useState<ValueDraft>(emptyValue);
  const ledger = useQuery({ queryKey: ["ledger", "ledger", ledgerId], queryFn: () => fetchLedger(ledgerId), enabled: ledgerId !== "" });
  const values = useQuery({ queryKey: ["ledger", "dimension-values", ledgerId], queryFn: () => fetchDimensionValues(ledgerId), enabled: ledgerId !== "" });
  const today = todayISO();

  const columns: DataTableColumn<DimensionValue>[] = [
    { key: "attribute", header: "Dimension", mono: true },
    { key: "value", header: "Value", mono: true },
    { key: "name", header: "Name" },
    { key: "activeFrom", header: "Active from", mono: true, render: (v) => v.activeFrom ?? "" },
    { key: "activeTo", header: "Active to", mono: true, render: (v) => v.activeTo ?? "" },
    { key: "status", header: "Status", render: (v) => <StatusPill status={valueStatus(v, today)} /> },
  ];
  const error = ledger.error ?? values.error;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dimension values"
        description="Registered values of the ledger's financial dimensions. Values that aren't registered are only checked for format."
      />
      <LedgerPicker id="dv-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={(id) => { setDraft(emptyValue); setLedgerId(id); }} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its dimension values.</p>
      ) : error ? (
        <Alert
          tone="danger"
          title="Could not load the dimension values"
          live="alert"
          action={<Button size="sm" onClick={() => { void ledger.refetch(); void values.refetch(); }}>Try again</Button>}
        >
          {(error as Error).message}
        </Alert>
      ) : (
        <>
          {ledger.data ? (
            ledger.data.dimensionAttributes.length > 0 ? (
              <ValueForm ledgerId={ledgerId} attributes={ledger.data.dimensionAttributes} draft={draft} onDraft={setDraft} existing={values.data ?? []} />
            ) : (
              <Alert tone="info" title="This ledger has no dimensions">
                Dimensions are set when the ledger is created.
              </Alert>
            )
          ) : null}
          <DataTable
            label="Dimension values"
            columns={columns}
            rows={values.data ?? []}
            getRowKey={(v) => `${v.attribute}|${v.value}`}
            loading={values.isPending || ledger.isPending}
            empty={{ title: "No registered values", description: "Add a value to give it a name, dates or a suspension." }}
            onRowOpen={(v) =>
              setDraft({ attribute: v.attribute, value: v.value, name: v.name, activeFrom: v.activeFrom ?? "", activeTo: v.activeTo ?? "", suspended: v.suspended })
            }
          />
          <p className={styles.note}>
            <Link to={`/ledger/structures${ledgerQuery(ledgerId)}`}>Account structures</Link> decide which dimensions each account needs.
          </p>
        </>
      )}
    </div>
  );
}

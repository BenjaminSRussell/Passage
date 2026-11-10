import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { deleteTaxCode, listJurisdictions, listTaxCodes, mechanisms, putTaxCode, type TaxCode, type TaxCodeInput } from "./api/client";
import { FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

const CODE = /^[A-Za-z0-9._-]{1,64}$/;
const PCT = /^\d{1,3}(\.\d{1,4})?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

const mechanismLabel: Record<(typeof mechanisms)[number], string> = {
  standard: "Standard",
  reverse_charge: "Reverse charge",
  use_tax: "Use tax",
};

export interface RateDraft {
  valid_from: string;
  valid_to: string;
  rate_pct: string;
}

export interface CodeDraft {
  code: string;
  name: string;
  jurisdiction: string;
  mechanism: (typeof mechanisms)[number];
  recoverable_pct: string;
  rates: RateDraft[];
}

const emptyDraft: CodeDraft = { code: "", name: "", jurisdiction: "", mechanism: "standard", recoverable_pct: "", rates: [{ valid_from: "", valid_to: "", rate_pct: "" }] };

/** Client checks that mirror PUT /codes/{code}; the service has the last word. */
export function validateCode(draft: CodeDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!CODE.test(draft.code.trim())) errors.code = "Use up to 64 letters, digits, dots, dashes or underscores.";
  if (draft.name.trim() === "" || draft.name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (draft.jurisdiction === "") errors.jurisdiction = "Choose the jurisdiction this tax belongs to.";
  if (draft.recoverable_pct.trim() !== "" && (!PCT.test(draft.recoverable_pct.trim()) || Number(draft.recoverable_pct) > 100)) errors.recoverable_pct = "Enter a percentage from 0 to 100, or leave it empty.";
  draft.rates.forEach((r, i) => {
    if (!DATE.test(r.valid_from)) errors[`rates.${i}.valid_from`] = "Enter the first day this rate applies.";
    else if (r.valid_to !== "" && (!DATE.test(r.valid_to) || r.valid_to < r.valid_from)) errors[`rates.${i}.valid_to`] = "The last day cannot be before the first day.";
    if (!PCT.test(r.rate_pct.trim()) || Number(r.rate_pct) > 100) errors[`rates.${i}.rate_pct`] = "Enter a rate from 0 to 100.";
  });
  return errors;
}

export function toInput(draft: CodeDraft): TaxCodeInput {
  return {
    name: draft.name.trim(),
    jurisdiction: draft.jurisdiction,
    mechanism: draft.mechanism,
    ...(draft.recoverable_pct.trim() === "" ? {} : { recoverable_pct: draft.recoverable_pct.trim() }),
    rates: draft.rates.map((r) => ({ valid_from: r.valid_from, ...(r.valid_to === "" ? {} : { valid_to: r.valid_to }), rate_pct: r.rate_pct.trim() })),
  };
}

/** The rate in force on a day, or undefined when none covers it. */
export function rateOn(code: TaxCode, isoDay: string): string | undefined {
  return code.rates.find((r) => r.valid_from <= isoDay && (!r.valid_to || r.valid_to >= isoDay))?.rate_pct;
}

export function TaxCodesPage() {
  const codes = useQuery({ queryKey: taxKey("codes"), queryFn: () => listTaxCodes() });
  const jurisdictions = useQuery({ queryKey: taxKey("jurisdictions"), queryFn: () => listJurisdictions() });
  const [draft, setDraft] = useState<CodeDraft>(emptyDraft);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [toDelete, setToDelete] = useState<TaxCode | null>(null);
  const names = useMemo(() => new Map((jurisdictions.data ?? []).map((j) => [j.code, j.name])), [jurisdictions.data]);
  const today = new Date().toISOString().slice(0, 10);

  const reset = () => {
    setDraft(emptyDraft);
    setEditing(false);
    setErrors({});
  };
  const save = useAction((d: CodeDraft) => putTaxCode(d.code.trim(), toInput(d)), [taxKey("codes")], reset);
  const remove = useAction((code: string) => deleteTaxCode(code), [taxKey("codes")]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCode(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(draft);
  }
  const shown = { ...fieldErrors(save.failure), ...errors };
  const setRate = (i: number, patch: Partial<RateDraft>) => setDraft({ ...draft, rates: draft.rates.map((r, n) => (n === i ? { ...r, ...patch } : r)) });

  const columns: DataTableColumn<TaxCode>[] = [
    { key: "name", header: "Tax" },
    { key: "code", header: "Code", mono: true },
    { key: "jurisdiction", header: "Jurisdiction", render: (c) => names.get(c.jurisdiction) ?? c.jurisdiction },
    { key: "mechanism", header: "Type", render: (c) => mechanismLabel[c.mechanism] },
    { key: "rate", header: "Rate today", align: "end", render: (c) => (rateOn(c, today) === undefined ? "None" : `${rateOn(c, today)}%`) },
    { key: "recoverable_pct", header: "Recoverable", align: "end", render: (c) => (c.recoverable_pct ? `${c.recoverable_pct}%` : "") },
    {
      key: "actions",
      header: "Actions",
      render: (c) => (
        <span className={styles.filters}>
          <Button
            size="sm"
            aria-label={`Edit ${c.name}`}
            onClick={() => {
              setDraft({
                code: c.code,
                name: c.name,
                jurisdiction: c.jurisdiction,
                mechanism: c.mechanism,
                recoverable_pct: c.recoverable_pct ?? "",
                rates: c.rates.map((r) => ({ valid_from: r.valid_from, valid_to: r.valid_to ?? "", rate_pct: r.rate_pct })),
              });
              setEditing(true);
              setErrors({});
              save.clear();
            }}
          >
            Edit
          </Button>
          <Button size="sm" variant="danger" aria-label={`Delete ${c.name}`} onClick={() => setToDelete(c)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Tax codes" description="Each tax has a code and a history of rates by date, so a document is taxed at the rate that applied on its date. Add a new rate with a start date instead of changing an old one." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit tax code" : "New tax code"}>
          <FormField id="code-code" label="Code" hint="For example OH-ST or VAT19." error={shown.code}>
            {(c) => <Input {...c} className={styles.mono} value={draft.code} readOnly={editing} onChange={(e) => setDraft({ ...draft, code: e.target.value })} invalid={Boolean(shown.code)} />}
          </FormField>
          <FormField id="code-name" label="Name" error={shown.name}>
            {(c) => <Input {...c} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} invalid={Boolean(shown.name)} />}
          </FormField>
          <FormField id="code-jurisdiction" label="Jurisdiction" error={shown.jurisdiction}>
            {(c) => (
              <Select {...c} value={draft.jurisdiction} onChange={(e) => setDraft({ ...draft, jurisdiction: e.target.value })} invalid={Boolean(shown.jurisdiction)}>
                <option value="">Choose a jurisdiction</option>
                {(jurisdictions.data ?? []).map((j) => (
                  <option key={j.code} value={j.code}>
                    {j.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="code-mechanism" label="Type">
            {(c) => (
              <Select {...c} value={draft.mechanism} onChange={(e) => setDraft({ ...draft, mechanism: e.target.value as CodeDraft["mechanism"] })}>
                {mechanisms.map((m) => (
                  <option key={m} value={m}>
                    {mechanismLabel[m]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="code-recoverable" label="Recoverable share (%)" optional hint="The part of input tax the company can claim back." error={shown.recoverable_pct}>
            {(c) => <Input {...c} inputMode="decimal" value={draft.recoverable_pct} onChange={(e) => setDraft({ ...draft, recoverable_pct: e.target.value })} invalid={Boolean(shown.recoverable_pct)} />}
          </FormField>
          <fieldset className={styles.group}>
            <legend className={styles.legend}>Rates by date</legend>
            {draft.rates.map((r, i) => (
              <div key={i} className={styles.form} style={{ gridColumn: "1 / -1" }}>
                <FormField id={`rate-from-${i}`} label={`Rate ${i + 1} starts`} error={shown[`rates.${i}.valid_from`]}>
                  {(c) => <Input {...c} type="date" value={r.valid_from} onChange={(e) => setRate(i, { valid_from: e.target.value })} invalid={Boolean(shown[`rates.${i}.valid_from`])} />}
                </FormField>
                <FormField id={`rate-to-${i}`} label={`Rate ${i + 1} ends`} optional error={shown[`rates.${i}.valid_to`]}>
                  {(c) => <Input {...c} type="date" value={r.valid_to} onChange={(e) => setRate(i, { valid_to: e.target.value })} invalid={Boolean(shown[`rates.${i}.valid_to`])} />}
                </FormField>
                <FormField id={`rate-pct-${i}`} label={`Rate ${i + 1} (%)`} error={shown[`rates.${i}.rate_pct`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={r.rate_pct} onChange={(e) => setRate(i, { rate_pct: e.target.value })} invalid={Boolean(shown[`rates.${i}.rate_pct`])} />}
                </FormField>
                {draft.rates.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" onClick={() => setDraft({ ...draft, rates: draft.rates.filter((_, n) => n !== i) })}>
                      Remove rate {i + 1}
                    </Button>
                  </div>
                ) : null}
              </div>
            ))}
            <div className={styles.formActions}>
              <Button size="sm" onClick={() => setDraft({ ...draft, rates: [...draft.rates, { valid_from: "", valid_to: "", rate_pct: "" }] })}>
                Add another rate
              </Button>
            </div>
          </fieldset>
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the tax code" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              {editing ? "Save tax code" : "Add tax code"}
            </Button>
            {editing ? <Button onClick={reset}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <FailureAlert title="Could not delete the tax code" failure={remove.failure} />
      {codes.isError ? (
        <LoadAlert title="Could not load the tax codes" error={codes.error} onRetry={() => void codes.refetch()} />
      ) : (
        <DataTable
          label="Tax codes"
          columns={columns}
          rows={codes.data ?? []}
          getRowKey={(c) => c.code}
          loading={codes.isPending}
          empty={{ title: "No tax codes yet", description: "Add a jurisdiction first, then a tax code with its rates." }}
        />
      )}
      {toDelete ? (
        <ConfirmDialog
          title={`Delete ${toDelete.name}?`}
          description="A tax code that a tax group or a VAT return box still uses cannot be deleted."
          confirmLabel="Delete tax code"
          cancelLabel="Keep it"
          onConfirm={() => {
            remove.run(toDelete.code);
            setToDelete(null);
          }}
          onCancel={() => setToDelete(null)}
        />
      ) : null}
    </div>
  );
}

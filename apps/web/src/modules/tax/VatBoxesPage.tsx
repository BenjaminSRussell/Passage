import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { listTaxCodes, listVatBoxes, putVatBoxes, type VatBox } from "./api/client";
import { FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

const BOX = /^[A-Za-z0-9._-]{1,16}$/;
type Direction = VatBox["codes"][number]["direction"];
const directions: { value: Direction; label: string }[] = [
  { value: "output", label: "Sales" },
  { value: "input", label: "Purchases" },
  { value: "use", label: "Use tax" },
];
const kindLabel: Record<VatBox["kind"], string> = { base: "Taxable amount", tax: "Tax amount", payable: "Tax to pay" };

export interface BoxDraft {
  box: string;
  label: string;
  kind: VatBox["kind"];
  sort: string;
  codes: { code: string; direction: Direction }[];
}

const emptyDraft: BoxDraft = { box: "", label: "", kind: "base", sort: "", codes: [] };

/** Client checks that mirror PUT /vat-boxes; the service has the last word. */
export function validateBox(draft: BoxDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!BOX.test(draft.box.trim())) errors.box = "Use up to 16 letters, digits, dots, dashes or underscores.";
  if (draft.label.trim() === "" || draft.label.length > 160) errors.label = "Enter a label of up to 160 characters.";
  if (draft.sort.trim() !== "" && !/^\d{1,4}$/.test(draft.sort.trim())) errors.sort = "Enter a whole number, or leave it empty.";
  if (draft.kind !== "payable" && draft.codes.length === 0) errors.codes = "Pick at least one tax code this box adds up.";
  return errors;
}

/** The box set with this box added or replaced, in order. */
export function withBox(boxes: VatBox[], draft: BoxDraft): VatBox[] {
  const next: VatBox = {
    box: draft.box.trim(),
    label: draft.label.trim(),
    kind: draft.kind,
    sort: draft.sort.trim() === "" ? (boxes.length + 1) * 10 : Number(draft.sort),
    codes: draft.kind === "payable" ? [] : draft.codes,
  };
  return [...boxes.filter((b) => b.box !== next.box), next].sort((a, b) => a.sort - b.sort);
}

export function VatBoxesPage() {
  const boxes = useQuery({ queryKey: taxKey("vat-boxes"), queryFn: () => listVatBoxes() });
  const codes = useQuery({ queryKey: taxKey("codes"), queryFn: () => listTaxCodes() });
  const [draft, setDraft] = useState<BoxDraft>(emptyDraft);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [toDelete, setToDelete] = useState<VatBox | null>(null);
  const codeNames = useMemo(() => new Map((codes.data ?? []).map((c) => [c.code, c.name])), [codes.data]);

  const reset = () => {
    setDraft(emptyDraft);
    setEditing(false);
    setErrors({});
  };
  const save = useAction((next: VatBox[]) => putVatBoxes(next), [taxKey("vat-boxes")], reset);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBox(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(withBox(boxes.data ?? [], draft));
  }
  const shown = { ...fieldErrors(save.failure), ...errors };
  const has = (code: string, direction: Direction) => draft.codes.some((c) => c.code === code && c.direction === direction);
  const toggle = (code: string, direction: Direction) =>
    setDraft({ ...draft, codes: has(code, direction) ? draft.codes.filter((c) => !(c.code === code && c.direction === direction)) : [...draft.codes, { code, direction }] });

  const columns: DataTableColumn<VatBox>[] = [
    { key: "box", header: "Box", mono: true },
    { key: "label", header: "What it shows" },
    { key: "kind", header: "Adds up", render: (b) => kindLabel[b.kind] },
    {
      key: "codes",
      header: "From",
      render: (b) =>
        b.kind === "payable"
          ? "All tax in the period"
          : b.codes.map((c) => `${codeNames.get(c.code) ?? c.code} (${directions.find((d) => d.value === c.direction)?.label.toLowerCase()})`).join(", "),
    },
    {
      key: "actions",
      header: "Actions",
      render: (b) => (
        <span className={styles.filters}>
          <Button
            size="sm"
            aria-label={`Edit box ${b.box}`}
            onClick={() => {
              setDraft({ box: b.box, label: b.label, kind: b.kind, sort: String(b.sort), codes: b.codes.map((c) => ({ ...c })) });
              setEditing(true);
              setErrors({});
              save.clear();
            }}
          >
            Edit
          </Button>
          <Button size="sm" variant="danger" aria-label={`Delete box ${b.box}`} onClick={() => setToDelete(b)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Return boxes" description="The boxes of the VAT return and the tax codes each one adds up. A box can add up the taxable amount, the tax amount or the tax to pay for the whole period." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit return box" : "New return box"}>
          <FormField id="box-number" label="Box" hint="The number printed on the return, for example 81." error={shown.box}>
            {(c) => <Input {...c} className={styles.mono} value={draft.box} readOnly={editing} onChange={(e) => setDraft({ ...draft, box: e.target.value })} invalid={Boolean(shown.box)} />}
          </FormField>
          <FormField id="box-label" label="What it shows" error={shown.label}>
            {(c) => <Input {...c} value={draft.label} onChange={(e) => setDraft({ ...draft, label: e.target.value })} invalid={Boolean(shown.label)} />}
          </FormField>
          <FormField id="box-kind" label="Adds up">
            {(c) => (
              <Select {...c} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as VatBox["kind"] })}>
                {Object.entries(kindLabel).map(([k, l]) => (
                  <option key={k} value={k}>
                    {l}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="box-sort" label="Position" optional hint="Boxes are listed in this order." error={shown.sort}>
            {(c) => <Input {...c} inputMode="numeric" value={draft.sort} onChange={(e) => setDraft({ ...draft, sort: e.target.value })} invalid={Boolean(shown.sort)} />}
          </FormField>
          {draft.kind !== "payable" ? (
            <fieldset className={styles.group}>
              <legend className={styles.legend}>Tax codes this box adds up</legend>
              {(codes.data ?? []).length === 0 ? <p className={styles.note}>Add tax codes first.</p> : null}
              {(codes.data ?? []).map((c) => (
                <div key={c.code} className={styles.filters}>
                  <span>{c.name}</span>
                  {directions.map((d) => (
                    <label key={d.value} className={styles.check}>
                      <input type="checkbox" checked={has(c.code, d.value)} onChange={() => toggle(c.code, d.value)} aria-label={`${c.name}, ${d.label.toLowerCase()}`} />
                      {d.label}
                    </label>
                  ))}
                </div>
              ))}
            </fieldset>
          ) : null}
          {shown.codes ? <p role="alert">{shown.codes}</p> : null}
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the return boxes" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              {editing ? "Save box" : "Add box"}
            </Button>
            {editing ? <Button onClick={reset}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      {boxes.isError ? (
        <LoadAlert title="Could not load the return boxes" error={boxes.error} onRetry={() => void boxes.refetch()} />
      ) : (
        <DataTable label="Return boxes" columns={columns} rows={boxes.data ?? []} getRowKey={(b) => b.box} loading={boxes.isPending} empty={{ title: "No return boxes yet", description: "Add the boxes of your VAT return and the tax codes each one adds up." }} />
      )}
      {toDelete ? (
        <ConfirmDialog
          title={`Delete box ${toDelete.box}?`}
          description="The box disappears from the VAT return. Recorded tax is not changed."
          confirmLabel="Delete box"
          cancelLabel="Keep it"
          onConfirm={() => {
            save.run((boxes.data ?? []).filter((b) => b.box !== toDelete.box));
            setToDelete(null);
          }}
          onCancel={() => setToDelete(null)}
        />
      ) : null}
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { deleteJurisdiction, jurisdictionKinds, listJurisdictions, putJurisdiction, type Jurisdiction } from "./api/client";
import { countryLabel, FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

const CODE = /^[A-Za-z0-9._-]{1,32}$/;

const emptyDraft: Jurisdiction = { code: "", name: "", kind: "state", country: "", parent_code: "" };

/** Client checks that mirror PUT /jurisdictions/{code}; the service has the last word. */
export function validateJurisdiction(draft: Jurisdiction): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!CODE.test(draft.code.trim())) errors.code = "Use up to 32 letters, digits, dots, dashes or underscores.";
  if (draft.name.trim() === "" || draft.name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!/^[A-Z]{2}$/.test(draft.country.trim().toUpperCase())) errors.country = "Enter the 2-letter country code, for example US.";
  return errors;
}

export interface TreeRow extends Jurisdiction {
  depth: number;
}

/** Roots first, each followed by its children, so the table reads as the tree. */
export function treeRows(all: Jurisdiction[]): TreeRow[] {
  const children = new Map<string, Jurisdiction[]>();
  for (const j of all) children.set(j.parent_code, [...(children.get(j.parent_code) ?? []), j]);
  const known = new Set(all.map((j) => j.code));
  const out: TreeRow[] = [];
  const walk = (parent: string, depth: number) => {
    for (const j of (children.get(parent) ?? []).sort((a, b) => a.name.localeCompare(b.name))) {
      out.push({ ...j, depth });
      walk(j.code, depth + 1);
    }
  };
  walk("", 0);
  // A jurisdiction whose parent is missing would otherwise vanish; show it at the top level.
  for (const j of all) if (j.parent_code !== "" && !known.has(j.parent_code)) out.push({ ...j, depth: 0 });
  return out;
}

export function JurisdictionsPage() {
  const list = useQuery({ queryKey: taxKey("jurisdictions"), queryFn: () => listJurisdictions() });
  const [draft, setDraft] = useState<Jurisdiction>(emptyDraft);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [toDelete, setToDelete] = useState<Jurisdiction | null>(null);
  const rows = useMemo(() => treeRows(list.data ?? []), [list.data]);
  const names = useMemo(() => new Map((list.data ?? []).map((j) => [j.code, j.name])), [list.data]);

  const reset = () => {
    setDraft(emptyDraft);
    setEditing(false);
    setErrors({});
  };
  const save = useAction((j: Jurisdiction) => putJurisdiction({ ...j, code: j.code.trim(), country: j.country.trim().toUpperCase() }), [taxKey("jurisdictions")], reset);
  const remove = useAction((code: string) => deleteJurisdiction(code), [taxKey("jurisdictions")]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJurisdiction(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(draft);
  }
  const shown = { ...fieldErrors(save.failure), ...errors };

  const columns: DataTableColumn<TreeRow>[] = [
    { key: "name", header: "Jurisdiction", render: (r) => <span style={{ paddingInlineStart: `${r.depth * 1.25}rem` }}>{r.name}</span> },
    { key: "code", header: "Code", mono: true },
    { key: "kind", header: "Level" },
    { key: "country", header: "Country", render: (r) => countryLabel(r.country) },
    { key: "parent_code", header: "Part of", render: (r) => (r.parent_code ? (names.get(r.parent_code) ?? r.parent_code) : "") },
    {
      key: "actions",
      header: "Actions",
      render: (r) => (
        <span className={styles.filters}>
          <Button
            size="sm"
            onClick={() => {
              setDraft({ ...r });
              setEditing(true);
              setErrors({});
              save.clear();
            }}
            aria-label={`Edit ${r.name}`}
          >
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setToDelete(r)} aria-label={`Delete ${r.name}`}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Jurisdictions" description="The tax authorities a company reports to. A country holds states, a state holds counties, a county holds cities. Tax codes belong to one jurisdiction." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit jurisdiction" : "New jurisdiction"}>
          <FormField id="jur-code" label="Code" hint="For example US-OH." error={shown.code}>
            {(c) => <Input {...c} className={styles.mono} value={draft.code} readOnly={editing} onChange={(e) => setDraft({ ...draft, code: e.target.value })} invalid={Boolean(shown.code)} />}
          </FormField>
          <FormField id="jur-name" label="Name" error={shown.name}>
            {(c) => <Input {...c} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} invalid={Boolean(shown.name)} />}
          </FormField>
          <FormField id="jur-kind" label="Level">
            {(c) => (
              <Select {...c} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as Jurisdiction["kind"] })}>
                {jurisdictionKinds.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="jur-country" label="Country" hint="The 2-letter code, such as US." error={shown.country}>
            {(c) => <Input {...c} className={styles.mono} value={draft.country} onChange={(e) => setDraft({ ...draft, country: e.target.value })} invalid={Boolean(shown.country)} />}
          </FormField>
          <FormField id="jur-parent" label="Part of" optional error={shown.parent_code}>
            {(c) => (
              <Select {...c} value={draft.parent_code} onChange={(e) => setDraft({ ...draft, parent_code: e.target.value })} invalid={Boolean(shown.parent_code)}>
                <option value="">Nothing (top level)</option>
                {(list.data ?? [])
                  .filter((j) => j.code !== draft.code)
                  .map((j) => (
                    <option key={j.code} value={j.code}>
                      {j.name}
                    </option>
                  ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the jurisdiction" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              {editing ? "Save jurisdiction" : "Add jurisdiction"}
            </Button>
            {editing ? <Button onClick={reset}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <FailureAlert title="Could not delete the jurisdiction" failure={remove.failure} />
      {list.isError ? (
        <LoadAlert title="Could not load the jurisdictions" error={list.error} onRetry={() => void list.refetch()} />
      ) : (
        <DataTable
          label="Jurisdictions"
          columns={columns}
          rows={rows}
          getRowKey={(r) => r.code}
          loading={list.isPending}
          empty={{ title: "No jurisdictions yet", description: "Add the country first, then the states, counties and cities below it." }}
        />
      )}
      {toDelete ? (
        <ConfirmDialog
          title={`Delete ${toDelete.name}?`}
          description="A jurisdiction that still has smaller jurisdictions or tax codes cannot be deleted."
          confirmLabel="Delete jurisdiction"
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

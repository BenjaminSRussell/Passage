import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { deleteTaxGroup, listTaxCodes, listTaxGroups, putTaxGroup, type GroupKind, type TaxGroup } from "./api/client";
import { FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

const CODE = /^[A-Za-z0-9._-]{1,64}$/;

const kinds: { kind: GroupKind; title: string; explain: string }[] = [
  { kind: "party", title: "Customer and vendor groups", explain: "Put customers and vendors in a group, for example domestic or EU businesses, and pick the taxes that apply to the group." },
  { kind: "item", title: "Item groups", explain: "Put items in a group, for example standard-rated goods or exempt services, and pick the taxes that apply to the group." },
];

export interface GroupDraft {
  code: string;
  name: string;
  tax_codes: string[];
}

const emptyDraft: GroupDraft = { code: "", name: "", tax_codes: [] };

/** Client checks that mirror PUT /groups/{kind}/{code}; the service has the last word. */
export function validateGroup(draft: GroupDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!CODE.test(draft.code.trim())) errors.code = "Use up to 64 letters, digits, dots, dashes or underscores.";
  if (draft.name.trim() === "" || draft.name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  return errors;
}

function GroupSection({ kind, title, explain }: { kind: GroupKind; title: string; explain: string }) {
  const groups = useQuery({ queryKey: taxKey("groups", kind), queryFn: () => listTaxGroups(kind) });
  const codes = useQuery({ queryKey: taxKey("codes"), queryFn: () => listTaxCodes() });
  const [draft, setDraft] = useState<GroupDraft>(emptyDraft);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [toDelete, setToDelete] = useState<TaxGroup | null>(null);
  const codeNames = useMemo(() => new Map((codes.data ?? []).map((c) => [c.code, c.name])), [codes.data]);

  const reset = () => {
    setDraft(emptyDraft);
    setEditing(false);
    setErrors({});
  };
  const save = useAction((d: GroupDraft) => putTaxGroup({ kind, code: d.code.trim(), name: d.name.trim(), tax_codes: d.tax_codes }), [taxKey("groups", kind)], reset);
  const remove = useAction((code: string) => deleteTaxGroup(kind, code), [taxKey("groups", kind)]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateGroup(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(draft);
  }
  const shown = { ...fieldErrors(save.failure), ...errors };
  const toggle = (code: string) =>
    setDraft({ ...draft, tax_codes: draft.tax_codes.includes(code) ? draft.tax_codes.filter((c) => c !== code) : [...draft.tax_codes, code] });

  const columns: DataTableColumn<TaxGroup>[] = [
    { key: "name", header: "Group" },
    { key: "code", header: "Code", mono: true },
    { key: "tax_codes", header: "Taxes", render: (g) => (g.tax_codes.length === 0 ? "None" : g.tax_codes.map((c) => codeNames.get(c) ?? c).join(", ")) },
    {
      key: "actions",
      header: "Actions",
      render: (g) => (
        <span className={styles.filters}>
          <Button
            size="sm"
            aria-label={`Edit ${g.name}`}
            onClick={() => {
              setDraft({ code: g.code, name: g.name, tax_codes: [...g.tax_codes] });
              setEditing(true);
              setErrors({});
              save.clear();
            }}
          >
            Edit
          </Button>
          <Button size="sm" variant="danger" aria-label={`Delete ${g.name}`} onClick={() => setToDelete(g)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  return (
    <section className={styles.stack} aria-label={title}>
      <PageHeader title={title} description={explain} />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={`${editing ? "Edit" : "New"} ${kind === "party" ? "customer and vendor" : "item"} group`}>
          <FormField id={`${kind}-group-code`} label="Code" error={shown.code}>
            {(c) => <Input {...c} className={styles.mono} value={draft.code} readOnly={editing} onChange={(e) => setDraft({ ...draft, code: e.target.value })} invalid={Boolean(shown.code)} />}
          </FormField>
          <FormField id={`${kind}-group-name`} label="Name" error={shown.name}>
            {(c) => <Input {...c} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} invalid={Boolean(shown.name)} />}
          </FormField>
          <fieldset className={styles.group}>
            <legend className={styles.legend}>Taxes that apply</legend>
            {(codes.data ?? []).length === 0 ? <p className={styles.note}>Add tax codes first.</p> : null}
            {(codes.data ?? []).map((c) => (
              <label key={c.code} className={styles.check}>
                <input type="checkbox" checked={draft.tax_codes.includes(c.code)} onChange={() => toggle(c.code)} />
                {c.name}
              </label>
            ))}
          </fieldset>
          {shown.tax_codes ? <p role="alert">{shown.tax_codes}</p> : null}
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the group" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              {editing ? "Save group" : "Add group"}
            </Button>
            {editing ? <Button onClick={reset}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <FailureAlert title="Could not delete the group" failure={remove.failure} />
      {groups.isError ? (
        <LoadAlert title={`Could not load the ${title.toLowerCase()}`} error={groups.error} onRetry={() => void groups.refetch()} />
      ) : (
        <DataTable label={title} columns={columns} rows={groups.data ?? []} getRowKey={(g) => g.code} loading={groups.isPending} empty={{ title: "No groups yet", description: "Add a group and choose the taxes that apply to it." }} />
      )}
      {toDelete ? (
        <ConfirmDialog
          title={`Delete ${toDelete.name}?`}
          description="Customers, vendors or items that still use this group will have no tax from it."
          confirmLabel="Delete group"
          cancelLabel="Keep it"
          onConfirm={() => {
            remove.run(toDelete.code);
            setToDelete(null);
          }}
          onCancel={() => setToDelete(null)}
        />
      ) : null}
    </section>
  );
}

export function TaxGroupsPage() {
  return (
    <div className={styles.stack}>
      {kinds.map((k) => (
        <GroupSection key={k.kind} {...k} />
      ))}
    </div>
  );
}

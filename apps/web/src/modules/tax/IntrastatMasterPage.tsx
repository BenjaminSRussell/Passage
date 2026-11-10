import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { deleteMaster, listMaster, masterKinds, putMaster, type MasterEntry, type MasterKind } from "./api/client";
import { countryLabel, FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

interface KindInfo {
  title: string;
  codeLabel: string;
  codeHint: string;
  pattern: RegExp;
}

export const kindInfo: Record<MasterKind, KindInfo> = {
  commodity: { title: "Commodity codes", codeLabel: "Commodity code", codeHint: "The 8-digit CN code, for example 84137030.", pattern: /^\d{8}$/ },
  transaction_code: { title: "Transaction codes", codeLabel: "Transaction code", codeHint: "1 or 2 digits, for example 11 for an outright sale.", pattern: /^\d{1,2}$/ },
  transport_mode: { title: "Transport modes", codeLabel: "Transport mode", codeHint: "1 digit, for example 3 for road.", pattern: /^\d$/ },
  port: { title: "Ports and offices", codeLabel: "Port code", codeHint: "Up to 10 letters or digits.", pattern: /^[A-Za-z0-9]{1,10}$/ },
  country: { title: "Country properties", codeLabel: "Country", codeHint: "The 2-letter code in capitals, for example DE.", pattern: /^[A-Z]{2}$/ },
  delivery_terms: { title: "Delivery terms", codeLabel: "Delivery terms", codeHint: "The 3-letter Incoterm in capitals, for example EXW.", pattern: /^[A-Z]{3}$/ },
};

export interface MasterDraft {
  code: string;
  description: string;
  supplementary_unit: string;
  eu_member: boolean;
  intrastat_code: string;
  country: string;
}

const emptyDraft: MasterDraft = { code: "", description: "", supplementary_unit: "", eu_member: true, intrastat_code: "", country: "" };

/** Client checks that mirror PUT /intrastat/master/{kind}/{code}; the service has the last word. */
export function validateMaster(kind: MasterKind, draft: MasterDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  const info = kindInfo[kind];
  if (!info.pattern.test(draft.code.trim())) errors.code = info.codeHint;
  if (draft.description.trim() === "" || draft.description.length > 200) errors.description = "Enter a description of up to 200 characters.";
  if (draft.supplementary_unit.length > 20) errors.supplementary_unit = "At most 20 characters.";
  if (kind === "port" && draft.country.trim() !== "" && !/^[A-Z]{2}$/.test(draft.country.trim().toUpperCase())) errors.country = "Enter the 2-letter country code.";
  return errors;
}

export function toEntry(kind: MasterKind, draft: MasterDraft): MasterEntry {
  const attrs: Record<string, string> = {};
  if (kind === "commodity" && draft.supplementary_unit.trim() !== "") attrs.supplementary_unit = draft.supplementary_unit.trim();
  if (kind === "country") {
    attrs.eu_member = String(draft.eu_member);
    if (draft.intrastat_code.trim() !== "") attrs.intrastat_code = draft.intrastat_code.trim().toUpperCase();
  }
  if (kind === "port" && draft.country.trim() !== "") attrs.country = draft.country.trim().toUpperCase();
  return { kind, code: draft.code.trim(), description: draft.description.trim(), attrs };
}

function details(e: MasterEntry): string {
  if (e.kind === "commodity") return e.attrs.supplementary_unit ? `Quantity in ${e.attrs.supplementary_unit}` : "";
  if (e.kind === "country") return [e.attrs.eu_member === "true" ? "EU member" : "Not an EU member", e.attrs.intrastat_code ? `Intrastat code ${e.attrs.intrastat_code}` : ""].filter(Boolean).join(", ");
  if (e.kind === "port") return e.attrs.country ? countryLabel(e.attrs.country) : "";
  return "";
}

export function IntrastatMasterPage() {
  const [kind, setKind] = useState<MasterKind>("commodity");
  const info = kindInfo[kind];
  const list = useQuery({ queryKey: taxKey("intrastat-master", kind), queryFn: () => listMaster(kind) });
  const [draft, setDraft] = useState<MasterDraft>(emptyDraft);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [toDelete, setToDelete] = useState<MasterEntry | null>(null);

  const reset = () => {
    setDraft(emptyDraft);
    setEditing(false);
    setErrors({});
  };
  const save = useAction((d: MasterDraft) => putMaster(toEntry(kind, d)), [taxKey("intrastat-master", kind)], reset);
  const remove = useAction((code: string) => deleteMaster(kind, code), [taxKey("intrastat-master", kind)]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateMaster(kind, draft);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(draft);
  }
  const shown = { ...fieldErrors(save.failure), ...errors };

  const columns: DataTableColumn<MasterEntry>[] = [
    { key: "code", header: info.codeLabel, mono: true },
    { key: "description", header: "Description" },
    { key: "details", header: "Details", render: details },
    {
      key: "actions",
      header: "Actions",
      render: (e) => (
        <span className={styles.filters}>
          <Button
            size="sm"
            aria-label={`Edit ${e.code}`}
            onClick={() => {
              setDraft({
                code: e.code,
                description: e.description,
                supplementary_unit: e.attrs.supplementary_unit ?? "",
                eu_member: e.attrs.eu_member !== "false",
                intrastat_code: e.attrs.intrastat_code ?? "",
                country: e.attrs.country ?? "",
              });
              setEditing(true);
              setErrors({});
              save.clear();
            }}
          >
            Edit
          </Button>
          <Button size="sm" variant="danger" aria-label={`Delete ${e.code}`} onClick={() => setToDelete(e)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Intrastat master data" description="The codes an Intrastat declaration is checked against. A line that uses a code missing here fails validation." />
      <div className={styles.filters}>
        <FormField id="master-kind" label="Kind of data">
          {(c) => (
            <Select
              {...c}
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as MasterKind);
                reset();
              }}
            >
              {masterKinds.map((k) => (
                <option key={k} value={k}>
                  {kindInfo[k].title}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={`${editing ? "Edit" : "New"} ${info.title.toLowerCase()} entry`}>
          <FormField id="master-code" label={info.codeLabel} hint={info.codeHint} error={shown.code}>
            {(c) => <Input {...c} className={styles.mono} value={draft.code} readOnly={editing} onChange={(e) => setDraft({ ...draft, code: e.target.value })} invalid={Boolean(shown.code)} />}
          </FormField>
          <FormField id="master-description" label="Description" error={shown.description}>
            {(c) => <Input {...c} value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} invalid={Boolean(shown.description)} />}
          </FormField>
          {kind === "commodity" ? (
            <FormField id="master-unit" label="Quantity unit" optional hint="Set when the declaration needs a quantity besides the weight, for example p/st for pieces." error={shown.supplementary_unit}>
              {(c) => <Input {...c} value={draft.supplementary_unit} onChange={(e) => setDraft({ ...draft, supplementary_unit: e.target.value })} invalid={Boolean(shown.supplementary_unit)} />}
            </FormField>
          ) : null}
          {kind === "country" ? (
            <>
              <FormField id="master-intrastat-code" label="Intrastat country code" optional hint="Only when it differs from the 2-letter code, for example EL for Greece.">
                {(c) => <Input {...c} className={styles.mono} value={draft.intrastat_code} onChange={(e) => setDraft({ ...draft, intrastat_code: e.target.value })} />}
              </FormField>
              <label className={styles.check}>
                <input type="checkbox" checked={draft.eu_member} onChange={(e) => setDraft({ ...draft, eu_member: e.target.checked })} />
                Member of the EU
              </label>
            </>
          ) : null}
          {kind === "port" ? (
            <FormField id="master-port-country" label="Country" optional error={shown.country}>
              {(c) => <Input {...c} className={styles.mono} value={draft.country} onChange={(e) => setDraft({ ...draft, country: e.target.value })} invalid={Boolean(shown.country)} />}
            </FormField>
          ) : null}
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the entry" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              {editing ? "Save entry" : "Add entry"}
            </Button>
            {editing ? <Button onClick={reset}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <FailureAlert title="Could not delete the entry" failure={remove.failure} />
      {list.isError ? (
        <LoadAlert title={`Could not load the ${info.title.toLowerCase()}`} error={list.error} onRetry={() => void list.refetch()} />
      ) : (
        <DataTable label={info.title} columns={columns} rows={list.data ?? []} getRowKey={(e) => e.code} loading={list.isPending} empty={{ title: `No ${info.title.toLowerCase()} yet`, description: "Add the codes your declarations use." }} />
      )}
      {toDelete ? (
        <ConfirmDialog
          title={`Delete ${toDelete.code}?`}
          description="Journal lines that use this code will fail validation until it is added again."
          confirmLabel="Delete entry"
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

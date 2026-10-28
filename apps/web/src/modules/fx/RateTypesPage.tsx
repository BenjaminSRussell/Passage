import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchRateTypes, putRateType, type RateType } from "./api/client";
import { ApiFailure, mapServerFields, useFocusFirstInvalid } from "./formSupport";
import styles from "./pages.module.css";
import { emptyRateType, normalizeCurrency, validateRateType, type Errors, type RateTypeValues } from "./validate";

const DUTY = "Maintain exchange rates";

const columns: DataTableColumn<RateType>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "allowInverse", header: "Inverse rates", render: (t) => (t.allowInverse ? "Allowed" : "Not allowed") },
  { key: "provider", header: "Provider", render: (t) => t.provider ?? "Manual entry" },
];

export function RateTypesPage() {
  const queryClient = useQueryClient();
  const form = useRef<HTMLFormElement>(null);
  const [values, setValues] = useState<RateTypeValues>(emptyRateType);
  const [editing, setEditing] = useState(false);
  const [errors, setErrors] = useState<Errors>({});
  const [saved, setSaved] = useState<string | null>(null);
  const types = useQuery({ queryKey: ["fx", "rate-types"], queryFn: () => fetchRateTypes() });
  const save = useMutation({
    mutationFn: (v: RateTypeValues) => putRateType(v.code, { name: v.name.trim(), allowInverse: v.allowInverse, provider: v.provider.trim() || undefined }),
    onSuccess: async (stored) => {
      setSaved(stored.code);
      setValues(emptyRateType);
      setEditing(false);
      await queryClient.invalidateQueries({ queryKey: ["fx", "rate-types"] });
    },
    onError: (error) => setErrors(mapServerFields(error, "", ["code", "name", "provider"])),
  });
  useFocusFirstInvalid(form, errors);

  function change(next: Partial<RateTypeValues>) {
    setValues({ ...values, ...next });
    setSaved(null);
    save.reset();
  }

  function edit(type: RateType) {
    setValues({ code: type.code, name: type.name, provider: type.provider ?? "", allowInverse: type.allowInverse });
    setEditing(true);
    setErrors({});
    setSaved(null);
    save.reset();
  }

  function startNew() {
    setValues(emptyRateType);
    setEditing(false);
    setErrors({});
    save.reset();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRateType(values);
    setErrors(found);
    if (Object.keys(found).length === 0) save.mutate({ ...values, code: values.code.trim() });
  }

  const fieldFailure = save.isError && Object.keys(mapServerFields(save.error, "", ["code", "name", "provider"])).length === 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rate types"
        description="A rate type groups rates that are used for the same purpose, such as closing or average rates. Saving a code that already exists updates it."
        actions={
          editing ? (
            <Button variant="secondary" onClick={startNew}>
              New rate type
            </Button>
          ) : null
        }
      />
      <Card title={editing ? `Edit ${values.code}` : "New rate type"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit rate type" : "New rate type"} ref={form}>
          <FormField id="type-code" label="Code" hint={editing ? "The code cannot be changed." : undefined} error={errors.code}>
            {(c) => <Input {...c} value={values.code} maxLength={32} placeholder="CLOSING" readOnly={editing} onChange={(e) => change({ code: normalizeCurrency(e.target.value) })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="type-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={values.name} placeholder="Month-end closing" onChange={(e) => change({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="type-provider" label="Provider" optional hint="Name of the provider that imports this type. Leave empty for manual entry." error={errors.provider}>
            {(c) => <Input {...c} value={values.provider} placeholder="ecb" onChange={(e) => change({ provider: e.target.value })} invalid={Boolean(errors.provider)} />}
          </FormField>
          <FormField id="type-inverse" label="Allow inverse rates" hint="When on, a missing EUR to USD rate can be derived from USD to EUR.">
            {(c) => <Input {...c} type="checkbox" checked={values.allowInverse} onChange={(e) => change({ allowInverse: e.target.checked })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.isPending}>
              {editing ? "Save changes" : "Create rate type"}
            </Button>
          </div>
        </form>
      </Card>
      {saved ? (
        <Alert tone="success" title={`Rate type ${saved} saved`} live="status" />
      ) : null}
      {fieldFailure ? <ApiFailure error={save.error} title="Could not save the rate type" duty={DUTY} /> : null}
      {types.isError ? (
        <ApiFailure error={types.error} title="Could not load the rate types" duty="View exchange rates" />
      ) : (
        <DataTable
          label="Rate types"
          columns={columns}
          rows={types.data ?? []}
          getRowKey={(t) => t.code}
          loading={types.isPending}
          onRowOpen={edit}
          empty={{ title: "No rate types yet", description: "Create one above before entering or importing rates." }}
        />
      )}
    </div>
  );
}

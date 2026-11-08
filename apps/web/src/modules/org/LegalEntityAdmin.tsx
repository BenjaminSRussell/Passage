import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, listLegalEntities, type LegalEntity, type Problem } from "./api/client";
import { listCountries, listCurrencies } from "./api/reference";
import { createLegalEntity, getLegalEntity, setLegalEntityAddress, setLegalEntityStatus, withFreshRowOnConflict } from "./api/structure";
import { AddressAction } from "./AddressAction";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { StatusAction, useRows } from "./referenceParts";
import { codeProblems, validateLegalEntity, type LegalEntityErrors, type LegalEntityValues } from "./legalEntityValues";
import { CopyLegalEntityForm, copySummary } from "./CopyLegalEntityForm";

export { validateLegalEntity, type LegalEntityValues };

function CreateLegalEntityForm({ onCreated }: { onCreated(): void }) {
  const countries = useRows((signal) => listCountries("active", { signal }), "countries");
  const currencies = useRows((signal) => listCurrencies("active", { signal }), "currencies");
  const [values, setValues] = useState<LegalEntityValues>({ code: "", name: "", country: "", currency: "", timeZone: "UTC" });
  const [errors, setErrors] = useState<LegalEntityErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateLegalEntity(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createLegalEntity({ ...values, code: values.code.toUpperCase(), name: values.name.trim(), timeZone: values.timeZone.trim() });
      setValues({ code: "", name: "", country: "", currency: "", timeZone: "UTC" });
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (codeProblems[problem.code]) {
        setErrors({ code: codeProblems[problem.code] });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof LegalEntityValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });
  const loadFailure = countries.error ?? currencies.error;

  return (
    <Card title="Add legal entity">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="le-code" label="Code" error={errors.code}>
          {(control) => <Input {...control} {...text("code")} maxLength={8} />}
        </FormField>
        <FormField id="le-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} {...text("name")} maxLength={120} />}
        </FormField>
        <FormField id="le-country" label="Country" error={errors.country}>
          {(control) => (
            <Select {...control} {...text("country")}>
              <option value="">Select a country</option>
              {countries.rows.map((row) => (
                <option key={row.code} value={row.code}>
                  {row.code} - {row.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="le-currency" label="Accounting currency" error={errors.currency}>
          {(control) => (
            <Select {...control} {...text("currency")}>
              <option value="">Select a currency</option>
              {currencies.rows.map((row) => (
                <option key={row.code} value={row.code}>
                  {row.code} - {row.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="le-time-zone" label="Time zone" hint="IANA name, for example America/Detroit." error={errors.timeZone}>
          {(control) => <Input {...control} {...text("timeZone")} />}
        </FormField>
        {loadFailure ? (
          <Alert tone="danger" title="Could not load the country and currency lists" live="alert">
            {loadFailure.message} ({loadFailure.code})
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not add the legal entity" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Add legal entity
          </Button>
        </div>
      </form>
    </Card>
  );
}

export function LegalEntitiesPage() {
  const { canWriteLegalEntity, canWriteAddress, canAdministerTenant, reload: reloadSwitcher } = useLegalEntity();
  const state = useRows((signal) => listLegalEntities(ALL, { signal }).then((page) => page.items), "legal-entities");
  const [copySource, setCopySource] = useState<LegalEntity | null>(null);
  const [copied, setCopied] = useState("");
  const columns: DataTableColumn<LegalEntity>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "country", header: "Country" },
    { key: "currency", header: "Accounting currency" },
    { key: "timeZone", header: "Time zone" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "version", header: "Version", align: "end" },
  ];
  if (canWriteLegalEntity) {
    columns.push({
      key: "actions",
      header: "Actions",
      render: (row) => {
        // Replace the row from the response (not merge): the service leaves a cleared field out of it.
        const refresh = (write: () => Promise<LegalEntity>) =>
          withFreshRowOnConflict(
            write,
            () => getLegalEntity(row.id),
            (fresh) => state.update((rows) => rows.map((item) => (item.id === row.id ? fresh : item))),
          );
        return (
          <div className={styles.rowActions}>
            {canWriteAddress ? (
              <AddressAction
                subject={`legal entity ${row.code}`}
                ownerType="legal_entity"
                ownerId={row.id}
                legalEntityId={row.id}
                addressId={row.primaryAddressId}
                setPointer={(addressId) => refresh(() => setLegalEntityAddress(row.id, row.version, addressId))}
              />
            ) : null}
            {canAdministerTenant ? (
              <Button
                size="sm"
                variant="ghost"
                aria-label={`Copy legal entity ${row.code}`}
                onClick={() => {
                  setCopied("");
                  setCopySource(row);
                }}
              >
                Copy
              </Button>
            ) : null}
            <StatusAction
              subject={`legal entity ${row.code}`}
              status={row.status}
              apply={async (next) => {
                await refresh(() => setLegalEntityStatus(row.id, row.version, next));
                reloadSwitcher();
              }}
            />
          </div>
        );
      },
    });
  }
  return (
    <>
      <PageHeader title="Legal entities" description="Companies of the tenant. The switcher refreshes after a new entity is added or one is deactivated." />
      {canWriteLegalEntity && canAdministerTenant ? <CreateLegalEntityForm
          onCreated={() => {
            state.reload();
            reloadSwitcher();
          }}
        /> : null}
      {copySource && canAdministerTenant ? (
        <CopyLegalEntityForm
          key={copySource.id}
          source={copySource}
          onCancel={() => setCopySource(null)}
          onCopied={(copy) => {
            setCopied(copySummary(copySource.code, copy));
            setCopySource(null);
            state.reload();
            reloadSwitcher();
          }}
        />
      ) : null}
      {copied ? (
        <Alert tone="success" title="Legal entity copied" live="status">
          {copied}
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load legal entities" live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable label="Legal entities" columns={columns} rows={state.rows} getRowKey={(row) => row.id} loading={state.loading} empty={{ title: "No legal entities" }} />
    </>
  );
}

import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listOperatingUnits,
  listSites,
  listWarehouses,
  type OperatingUnit,
  type Problem,
  type Site,
  type Warehouse,
} from "./api/client";
import {
  createOperatingUnit,
  createSite,
  createWarehouse,
  getOperatingUnit,
  getSite,
  getWarehouse,
  operatingUnitTypes,
  setOperatingUnitStatus,
  setSiteStatus,
  setWarehouseStatus,
  warehouseTypes,
  type OperatingUnitType,
  type WarehouseType,
} from "./api/write";
import { withFreshRowOnConflict } from "./api/structure";
import { useLegalEntity } from "./legalEntityContext";
import { ScopedListPage, type ListControls } from "./pages";
import styles from "./pages.module.css";
import { StatusAction, useRows } from "./referenceParts";

const dash = "—";
const ouCode = /^[A-Za-z0-9][A-Za-z0-9_-]{1,19}$/;
const shortCode = /^[A-Za-z0-9][A-Za-z0-9_-]{0,9}$/;

type Errors<K extends string> = Partial<Record<K, string>>;

function checkName(name: string): string | undefined {
  return name.trim() === "" || name.trim().length > 120 ? "Enter a name of 1 to 120 characters." : undefined;
}

function compact<K extends string>(errors: Errors<K>): Errors<K> {
  return Object.fromEntries(Object.entries(errors).filter(([, message]) => message !== undefined)) as Errors<K>;
}

export interface OperatingUnitValues {
  type: "" | OperatingUnitType;
  code: string;
  name: string;
  defaultLegalEntityId: string;
}

export function validateOperatingUnit({ type, code, name }: OperatingUnitValues): Errors<"type" | "code" | "name"> {
  return compact({
    type: type === "" ? "Select a type." : undefined,
    code: ouCode.test(code) ? undefined : "Use 2 to 20 letters, digits, - or _.",
    name: checkName(name),
  });
}

export interface SiteValues {
  legalEntityId: string;
  code: string;
  name: string;
  timeZone: string;
}

export function validateSite({ legalEntityId, code, name }: SiteValues): Errors<"legalEntityId" | "code" | "name"> {
  return compact({
    legalEntityId: legalEntityId === "" ? "Select a legal entity." : undefined,
    code: shortCode.test(code) ? undefined : "Use 1 to 10 letters, digits, - or _.",
    name: checkName(name),
  });
}

export interface WarehouseValues {
  siteId: string;
  code: string;
  name: string;
  type: WarehouseType;
  timeZone: string;
}

export function validateWarehouse({ siteId, code, name }: WarehouseValues): Errors<"siteId" | "code" | "name"> {
  return compact({
    siteId: siteId === "" ? "Select a site." : undefined,
    code: shortCode.test(code) ? undefined : "Use 1 to 10 letters, digits, - or _.",
    name: checkName(name),
  });
}

const label = (value: string) => value.replace(/_/g, " ");

interface CreateFormProps<V, K extends string> {
  title: string;
  subject: string;
  initial: V;
  validate(values: V): Errors<K>;
  send(values: V): Promise<unknown>;
  duplicate: { code: string; field: K; message: string };
  onCreated(): void;
  fields(args: { values: V; errors: Errors<K>; text(key: keyof V & string): { value: string; onChange(event: { target: { value: string } }): void }; set(next: V): void }): React.ReactNode;
}

function CreateForm<V, K extends string>({ title, subject, initial, validate, send, duplicate, onCreated, fields }: CreateFormProps<V, K>) {
  const [values, setValues] = useState<V>(initial);
  const [errors, setErrors] = useState<Errors<K>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validate(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await send(values);
      setValues(initial);
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === duplicate.code) {
        setErrors({ [duplicate.field]: duplicate.message } as Errors<K>);
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof V & string) => ({
    value: String(values[key] ?? ""),
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <Card title={title}>
      <form onSubmit={submit} noValidate className={styles.form}>
        {fields({ values, errors, text, set: setValues })}
        {failure ? (
          <Alert tone="danger" title={`Could not add the ${subject}`} live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            {title}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function useEntityCode(): (id?: string) => string {
  const { entities } = useLegalEntity();
  return (id) => (id ? (entities.find((entity) => entity.id === id)?.code ?? id) : dash);
}

function EntityOptions() {
  const { entities } = useLegalEntity();
  return (
    <>
      {entities.map((entity) => (
        <option key={entity.id} value={entity.id}>
          {entity.code} - {entity.name}
        </option>
      ))}
    </>
  );
}

function statusColumns<T extends { id: string; code: string; status: string; version: number }>(
  canWrite: boolean,
  controls: ListControls<T>,
  noun: string,
  write: { set(row: T, status: "active" | "inactive"): Promise<T>; get(row: T): Promise<T> },
): DataTableColumn<T>[] {
  const columns: DataTableColumn<T>[] = [{ key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> }];
  if (canWrite) {
    columns.push({
      key: "actions",
      header: "Actions",
      render: (row) => (
        <StatusAction
          subject={`${noun} ${row.code}`}
          status={row.status as "active" | "inactive"}
          apply={(next) =>
            withFreshRowOnConflict(
              () => write.set(row, next),
              () => write.get(row),
              (fresh) => controls.update((rows) => rows.map((item) => (item.id === row.id ? { ...item, ...fresh } : item))),
            )
          }
        />
      ),
    });
  }
  return columns;
}

const duplicateOf = (code: string, message: string) => ({ code, field: "code" as const, message });

export function OperatingUnitsPage() {
  const entityCode = useEntityCode();
  const { canWriteOperatingUnit } = useLegalEntity();
  return (
    <ScopedListPage<OperatingUnit>
      title="Operating units"
      label="Operating units"
      emptyTitle="No operating units"
      getRowKey={(row) => row.id}
      load={listOperatingUnits}
      columns={(controls) => [
        { key: "code", header: "Code", mono: true },
        { key: "name", header: "Name" },
        { key: "type", header: "Type", render: (row) => label(row.type) },
        { key: "defaultLegalEntityId", header: "Default legal entity", render: (row) => entityCode(row.defaultLegalEntityId) },
        ...statusColumns(canWriteOperatingUnit, controls, "operating unit", {
          set: (row, status) => setOperatingUnitStatus(row.id, row.version, status),
          get: (row) => getOperatingUnit(row.id),
        }),
      ]}
      toolbar={({ reload }) =>
        canWriteOperatingUnit ? (
          <CreateForm<OperatingUnitValues, "type" | "code" | "name">
            title="Add operating unit"
            subject="operating unit"
            initial={{ type: "", code: "", name: "", defaultLegalEntityId: "" }}
            validate={validateOperatingUnit}
            send={(values) =>
              createOperatingUnit({
                type: values.type as OperatingUnitType,
                code: values.code.toUpperCase(),
                name: values.name.trim(),
                defaultLegalEntityId: values.defaultLegalEntityId === "" ? undefined : values.defaultLegalEntityId,
              })
            }
            duplicate={duplicateOf("ORG-OU-CODE-DUPLICATE", "An operating unit of this type already has this code.")}
            onCreated={reload}
            fields={({ errors, text }) => (
              <>
                <FormField id="ou-type" label="Type" error={errors.type}>
                  {(control) => (
                    <Select {...control} {...text("type")}>
                      <option value="">Select a type</option>
                      {operatingUnitTypes.map((type) => (
                        <option key={type} value={type}>
                          {label(type)}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id="ou-code" label="Code" error={errors.code}>
                  {(control) => <Input {...control} {...text("code")} maxLength={20} />}
                </FormField>
                <FormField id="ou-name" label="Name" error={errors.name}>
                  {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                </FormField>
                <FormField id="ou-legal-entity" label="Default legal entity" optional>
                  {(control) => (
                    <Select {...control} {...text("defaultLegalEntityId")}>
                      <option value="">None</option>
                      <EntityOptions />
                    </Select>
                  )}
                </FormField>
              </>
            )}
          />
        ) : null
      }
    />
  );
}

export function SitesPage() {
  const entityCode = useEntityCode();
  const { canWriteSite } = useLegalEntity();
  return (
    <ScopedListPage<Site>
      title="Sites"
      label="Sites"
      emptyTitle="No sites"
      getRowKey={(row) => row.id}
      load={listSites}
      columns={(controls) => [
        { key: "code", header: "Code", mono: true },
        { key: "name", header: "Name" },
        { key: "legalEntityId", header: "Legal entity", render: (row) => entityCode(row.legalEntityId) },
        { key: "timeZone", header: "Time zone" },
        { key: "defaultWarehouseId", header: "Default warehouse", render: (row) => row.defaultWarehouseId ?? dash },
        ...statusColumns(canWriteSite, controls, "site", {
          set: (row, status) => setSiteStatus(row.legalEntityId, row.id, row.version, status),
          get: (row) => getSite(row.legalEntityId, row.id),
        }),
      ]}
      toolbar={({ reload }) =>
        canWriteSite ? (
          <CreateForm<SiteValues, "legalEntityId" | "code" | "name">
            title="Add site"
            subject="site"
            initial={{ legalEntityId: "", code: "", name: "", timeZone: "" }}
            validate={validateSite}
            send={(values) =>
              createSite({
                legalEntityId: values.legalEntityId,
                code: values.code.toUpperCase(),
                name: values.name.trim(),
                timeZone: values.timeZone.trim() === "" ? undefined : values.timeZone.trim(),
              })
            }
            duplicate={duplicateOf("ORG-SITE-CODE-DUPLICATE", "A site with this code already exists in the legal entity.")}
            onCreated={reload}
            fields={({ errors, text }) => (
              <>
                <FormField id="site-legal-entity" label="Site legal entity" error={errors.legalEntityId}>
                  {(control) => (
                    <Select {...control} {...text("legalEntityId")}>
                      <option value="">Select a legal entity</option>
                      <EntityOptions />
                    </Select>
                  )}
                </FormField>
                <FormField id="site-code" label="Code" error={errors.code}>
                  {(control) => <Input {...control} {...text("code")} maxLength={10} />}
                </FormField>
                <FormField id="site-name" label="Name" error={errors.name}>
                  {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                </FormField>
                <FormField id="site-time-zone" label="Time zone" optional hint="IANA name; defaults to the legal entity's.">
                  {(control) => <Input {...control} {...text("timeZone")} />}
                </FormField>
              </>
            )}
          />
        ) : null
      }
    />
  );
}

function siteChoices(sites: Site[]): Site[] {
  return sites.filter((site) => site.status === "active");
}

export function WarehousesPage() {
  const { canWriteSite, scope } = useLegalEntity();
  // Sites in the selected scope: an ALL read needs the all-scope duty, so a caller without it would get no sites.
  const sites = useRows((signal) => (scope === "" ? Promise.resolve([]) : listSites(scope, { signal }).then((page) => page.items)), `sites:${scope}`);
  const siteOf = (id: string) => sites.rows.find((site) => site.id === id);
  const siteCode = (id: string) => siteOf(id)?.code ?? id;
  return (
    <ScopedListPage<Warehouse>
      title="Warehouses"
      label="Warehouses"
      emptyTitle="No warehouses"
      getRowKey={(row) => row.id}
      load={listWarehouses}
      columns={(controls) => [
        { key: "code", header: "Code", mono: true },
        { key: "name", header: "Name" },
        { key: "siteId", header: "Site", render: (row) => siteCode(row.siteId) },
        { key: "type", header: "Type" },
        ...statusColumns(canWriteSite, controls, "warehouse", {
          set: (row, status) => setWarehouseStatus(row.legalEntityId, row.id, row.version, status),
          get: (row) => getWarehouse(row.legalEntityId, row.id),
        }),
      ]}
      toolbar={({ reload }) =>
        canWriteSite ? (
          <>
            {sites.error ? (
              <Alert tone="danger" title="Could not load the sites" live="alert">
                {sites.error.message} ({sites.error.code})
              </Alert>
            ) : null}
            <CreateForm<WarehouseValues, "siteId" | "code" | "name">
              title="Add warehouse"
              subject="warehouse"
              initial={{ siteId: "", code: "", name: "", type: "standard", timeZone: "" }}
              validate={validateWarehouse}
              send={(values) =>
                createWarehouse(siteOf(values.siteId)?.legalEntityId ?? "", {
                  siteId: values.siteId,
                  code: values.code.toUpperCase(),
                  name: values.name.trim(),
                  type: values.type,
                  timeZone: values.timeZone.trim() === "" ? undefined : values.timeZone.trim(),
                })
              }
              duplicate={duplicateOf("ORG-WH-CODE-DUPLICATE", "A warehouse with this code already exists in the legal entity.")}
              onCreated={reload}
              fields={({ errors, text }) => (
                <>
                  <FormField id="wh-site" label="Site" error={errors.siteId}>
                    {(control) => (
                      <Select {...control} {...text("siteId")}>
                        <option value="">Select a site</option>
                        {siteChoices(sites.rows).map((site) => (
                          <option key={site.id} value={site.id}>
                            {site.code} - {site.name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id="wh-code" label="Code" error={errors.code}>
                    {(control) => <Input {...control} {...text("code")} maxLength={10} />}
                  </FormField>
                  <FormField id="wh-name" label="Name" error={errors.name}>
                    {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                  </FormField>
                  <FormField id="wh-type" label="Type">
                    {(control) => (
                      <Select {...control} {...text("type")}>
                        {warehouseTypes.map((type) => (
                          <option key={type} value={type}>
                            {label(type)}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id="wh-time-zone" label="Time zone" optional hint="IANA name; defaults to the site's.">
                    {(control) => <Input {...control} {...text("timeZone")} />}
                  </FormField>
                </>
              )}
            />
          </>
        ) : null
      }
    />
  );
}

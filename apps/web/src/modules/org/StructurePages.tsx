import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listLocations,
  listOperatingUnits,
  listSites,
  listWarehouses,
  type Location,
  type OperatingUnit,
  type Problem,
  type Site,
  type Warehouse,
} from "./api/client";
import {
  createLocation,
  createOperatingUnit,
  createSite,
  createWarehouse,
  getLocation,
  getOperatingUnit,
  getSite,
  getWarehouse,
  locationTypes,
  operatingUnitTypes,
  setLocationStatus,
  setOperatingUnitStatus,
  setSiteStatus,
  setWarehouseStatus,
  updateLocation,
  updateOperatingUnit,
  updateSite,
  updateWarehouse,
  warehouseTypes,
  type LocationType,
  type OperatingUnitType,
  type WarehouseType,
} from "./api/write";
import { listHierarchyTypes } from "./api/hierarchy";
import { listFiscalCalendars, withFreshRowOnConflict } from "./api/structure";
import { useLegalEntity } from "./legalEntityContext";
import { ScopedListPage, type ListControls } from "./pages";
import styles from "./pages.module.css";
import { AddressAction } from "./AddressAction";
import { EditAction } from "./RowEdit";
import { FilterSelect, StatusAction, useRows } from "./referenceParts";
import { parentCell, useUnitParents } from "./unitParents";
import { ProblemMessage } from "../../shell/errors";

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

export interface LocationValues {
  warehouseId: string;
  code: string;
  name: string;
  type: LocationType;
}

const locationCode = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,19}$/;

export function validateLocation({ warehouseId, code, name }: LocationValues): Errors<"warehouseId" | "code" | "name"> {
  return compact({
    warehouseId: warehouseId === "" ? "Select a warehouse." : undefined,
    code: locationCode.test(code) ? undefined : "Use 1 to 20 letters, digits, - _ or .",
    name: checkName(name),
  });
}

// Edit forms: the name is required and a blank default (legal entity, warehouse) clears it. The service cannot clear a
// time zone (it accepts only an IANA name), so the edit form requires one rather than silently dropping a blank.
export function validateEditName({ name }: { name: string }): Errors<"name"> {
  return compact({ name: checkName(name) });
}

function checkTimeZone(zone: string): string | undefined {
  const message = "Enter an IANA time zone such as America/Detroit.";
  if (zone.trim() === "") {
    return message;
  }
  try {
    new Intl.DateTimeFormat("en", { timeZone: zone.trim() });
    return undefined;
  } catch {
    return message;
  }
}

export function validateEditZoned({ name, timeZone }: { name: string; timeZone: string }): Errors<"name" | "timeZone"> {
  return compact({ name: checkName(name), timeZone: checkTimeZone(timeZone) });
}

// A trimmed text field, or undefined (left out of the request) when the user did not change it.
const trimmed = (value: string | undefined) => (value === undefined ? undefined : value.trim());

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
            <ProblemMessage problem={failure} />
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

// EntityOptions lists the active legal entities; current (an edit form's saved value) is kept as an option even when
// it is no longer active, so the select shows what the row really holds.
function EntityOptions({ current }: { current?: string }) {
  const { entities } = useLegalEntity();
  return (
    <>
      {current && !entities.some((entity) => entity.id === current) ? <option value={current}>{current} (inactive)</option> : null}
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
  edit?: (row: T, save: (change: () => Promise<T>) => Promise<void>) => ReactNode,
  address?: (row: T, save: (change: () => Promise<T>) => Promise<void>) => ReactNode,
): DataTableColumn<T>[] {
  const columns: DataTableColumn<T>[] = [{ key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> }];
  if (canWrite) {
    columns.push({
      key: "actions",
      header: "Actions",
      render: (row) => {
        const apply = (change: () => Promise<T>) =>
          withFreshRowOnConflict(
            change,
            () => write.get(row),
            // Replace the row, not merge into it: the service leaves a cleared default out of the response
            // (omitempty), so a merge would keep showing the old default.
            (fresh) => controls.update((rows) => rows.map((item) => (item.id === row.id ? fresh : item))),
          );
        return (
          <div className={styles.rowActions}>
            {edit ? edit(row, apply) : null}
            {address ? address(row, apply) : null}
            <StatusAction
              subject={`${noun} ${row.code}`}
              status={row.status as "active" | "inactive"}
              apply={(next) => apply(() => write.set(row, next))}
            />
          </div>
        );
      },
    });
  }
  return columns;
}

const duplicateOf = (code: string, message: string) => ({ code, field: "code" as const, message });

export function OperatingUnitsPage() {
  const entityCode = useEntityCode();
  const { canWriteOperatingUnit, entities } = useLegalEntity();
  // The Parent column reads one hierarchy type's version in force today; units name their parents only there.
  const [hierarchyTypeId, setHierarchyTypeId] = useState("");
  const [units, setUnits] = useState<OperatingUnit[]>([]);
  const hierarchyTypes = useRows((signal) => listHierarchyTypes({ status: "active" }, { signal }), "ou-parent-types");
  const parents = useUnitParents(hierarchyTypeId);
  const codes = useMemo(
    () => new Map<string, string>([...entities.map((e) => [e.id, e.code] as const), ...units.map((u) => [u.id, u.code] as const)]),
    [entities, units],
  );
  return (
    <ScopedListPage<OperatingUnit>
      title="Operating units"
      label="Operating units"
      emptyTitle="No operating units"
      getRowKey={(row) => row.id}
      load={(scope, options) =>
        listOperatingUnits(scope, options).then((page) => {
          setUnits(page.items);
          return page;
        })
      }
      columns={(controls) => [
        { key: "code", header: "Code", mono: true },
        { key: "name", header: "Name" },
        { key: "type", header: "Type", render: (row) => label(row.type) },
        { key: "defaultLegalEntityId", header: "Default legal entity", render: (row) => entityCode(row.defaultLegalEntityId) },
        { key: "parent", header: "Parent", render: (row) => parentCell(parents, row.id, (id) => codes.get(id)) },
        ...statusColumns(canWriteOperatingUnit, controls, "operating unit", {
          set: (row, status) => setOperatingUnitStatus(row.id, row.version, status),
          get: (row) => getOperatingUnit(row.id),
        }, (row, save) => (
          <EditAction<{ name: string; defaultLegalEntityId: string }, "name" | "defaultLegalEntityId">
            subject={`operating unit ${row.code}`}
            initial={{ name: row.name, defaultLegalEntityId: row.defaultLegalEntityId ?? "" }}
            validate={validateEditName}
            fieldCodes={{
              "ORG-OU-LEGAL-ENTITY-INVALID": { field: "defaultLegalEntityId", message: "Choose an active legal entity you can access." },
            }}
            save={(change) =>
              save(() =>
                updateOperatingUnit(row.id, row.version, {
                  name: trimmed(change.name),
                  defaultLegalEntityId: change.defaultLegalEntityId,
                }),
              )
            }
            fields={({ errors, text }) => (
              <>
                <FormField id={`ou-edit-name-${row.id}`} label="Name" error={errors.name}>
                  {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                </FormField>
                <FormField id={`ou-edit-le-${row.id}`} label="Default legal entity" optional error={errors.defaultLegalEntityId}>
                  {(control) => (
                    <Select {...control} {...text("defaultLegalEntityId")}>
                      <option value="">None</option>
                      <EntityOptions current={row.defaultLegalEntityId} />
                    </Select>
                  )}
                </FormField>
              </>
            )}
          />
        )),
      ]}
      toolbar={({ reload }) => (
        <>
          <ParentPicker
            typeId={hierarchyTypeId}
            onChange={setHierarchyTypeId}
            types={hierarchyTypes.rows}
            typesError={hierarchyTypes.error}
            parents={parents}
          />
          {canWriteOperatingUnit ? (
          <>
            <p>
              <Link to="/org/operating-units/import">Import from CSV</Link>
            </p>
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
          </>
          ) : null}
        </>
      )}
    />
  );
}

function ParentPicker({
  typeId,
  onChange,
  types,
  typesError,
  parents,
}: {
  typeId: string;
  onChange(id: string): void;
  types: { id: string; code: string; name: string }[];
  typesError: Problem | null;
  parents: ReturnType<typeof useUnitParents>;
}) {
  return (
    <>
      <FilterSelect
        id="ou-parent-hierarchy"
        label="Parent from hierarchy"
        value={typeId}
        options={[{ value: "", label: "None" }, ...types.map((type) => ({ value: type.id, label: `${type.code} - ${type.name}` }))]}
        onChange={onChange}
      />
      {typesError ? (
        <Alert tone="danger" title="Could not load hierarchy types" live="alert">
          <ProblemMessage problem={typesError} />
        </Alert>
      ) : null}
      {parents.status === "error" ? (
        <Alert tone="danger" title="Could not load the hierarchy" live="alert">
          <ProblemMessage problem={parents.error} />
        </Alert>
      ) : null}
      {parents.status === "ready" && parents.versionNo === null ? (
        <Alert tone="info" title="No version in force today">
          This hierarchy has no published version in force today, so no parents are shown.
        </Alert>
      ) : null}
    </>
  );
}

export function SitesPage() {
  const entityCode = useEntityCode();
  const { canWriteSite, canWriteAddress, scope } = useLegalEntity();
  // Warehouses in the selected scope, for the default-warehouse choice (ALL needs the all-scope duty).
  const warehouses = useRows(
    (signal) => (scope === "" || !canWriteSite ? Promise.resolve([]) : listWarehouses(scope, { signal }).then((page) => page.items)),
    `site-warehouses:${scope}:${canWriteSite}`,
  );
  // Fiscal calendars are tenant-level; only a caller who can edit a site needs the choice.
  const calendars = useRows((signal) => (canWriteSite ? listFiscalCalendars({ signal }) : Promise.resolve([])), `site-calendars:${canWriteSite}`);
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
        }, (row, save) => (
          <EditAction<{ name: string; timeZone: string; defaultWarehouseId: string; fiscalCalendarId: string }, "name" | "timeZone" | "defaultWarehouseId" | "fiscalCalendarId">
            subject={`site ${row.code}`}
            initial={{ name: row.name, timeZone: row.timeZone, defaultWarehouseId: row.defaultWarehouseId ?? "", fiscalCalendarId: row.fiscalCalendarId ?? "" }}
            validate={validateEditZoned}
            fieldCodes={{
              "ORG-SITE-DEFAULT-WAREHOUSE-INVALID": { field: "defaultWarehouseId", message: "Choose an active warehouse of this site." },
              "ORG-SITE-CALENDAR-INVALID": { field: "fiscalCalendarId", message: "Choose an active fiscal calendar with a generated year." },
            }}
            save={(change) =>
              save(() =>
                updateSite(row.legalEntityId, row.id, row.version, {
                  name: trimmed(change.name),
                  timeZone: trimmed(change.timeZone),
                  defaultWarehouseId: change.defaultWarehouseId,
                  fiscalCalendarId: change.fiscalCalendarId,
                }),
              )
            }
            fields={({ errors, text }) => {
              const choices = warehouses.rows.filter((wh) => wh.siteId === row.id && (wh.status === "active" || wh.id === row.defaultWarehouseId));
              const current = row.defaultWarehouseId;
              const currentCalendar = row.fiscalCalendarId;
              // The service assigns only an active calendar with at least one generated year (ORG-SITE-CALENDAR-INVALID otherwise).
              const calendarChoices = calendars.rows.filter((cal) => (cal.status === "active" && (cal.generatedYears?.length ?? 0) > 0) || cal.id === currentCalendar);
              return (
                <>
                  <FormField id={`site-edit-name-${row.id}`} label="Name" error={errors.name}>
                    {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                  </FormField>
                  <FormField id={`site-edit-tz-${row.id}`} label="Time zone" hint="IANA name." error={errors.timeZone}>
                    {(control) => <Input {...control} {...text("timeZone")} />}
                  </FormField>
                  <FormField
                    id={`site-edit-wh-${row.id}`}
                    label="Default warehouse"
                    optional
                    hint={warehouses.error ? `Warehouses could not be loaded (${warehouses.error.code}).` : undefined}
                    error={errors.defaultWarehouseId}
                  >
                    {(control) => (
                      <Select {...control} {...text("defaultWarehouseId")}>
                        <option value="">None</option>
                        {current && !choices.some((wh) => wh.id === current) ? <option value={current}>{current}</option> : null}
                        {choices.map((wh) => (
                          <option key={wh.id} value={wh.id}>
                            {wh.code} - {wh.name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField
                    id={`site-edit-cal-${row.id}`}
                    label="Fiscal calendar"
                    optional
                    hint={calendars.error ? `Fiscal calendars could not be loaded (${calendars.error.code}).` : "Blank uses the legal entity's calendar."}
                    error={errors.fiscalCalendarId}
                  >
                    {(control) => (
                      <Select {...control} {...text("fiscalCalendarId")}>
                        <option value="">None</option>
                        {currentCalendar && !calendarChoices.some((cal) => cal.id === currentCalendar) ? <option value={currentCalendar}>{currentCalendar}</option> : null}
                        {calendarChoices.map((cal) => (
                          <option key={cal.id} value={cal.id}>
                            {cal.code} - {cal.name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                </>
              );
            }}
          />
        ), canWriteAddress ? (row, save) => (
          <AddressAction
            subject={`site ${row.code}`}
            ownerType="site"
            ownerId={row.id}
            legalEntityId={row.legalEntityId}
            addressId={row.addressId}
            setPointer={(addressId) => save(() => updateSite(row.legalEntityId, row.id, row.version, { addressId }))}
          />
        ) : undefined),
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
  const { canWriteSite, canWriteAddress, scope } = useLegalEntity();
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
        }, (row, save) => (
          <EditAction<{ name: string; type: WarehouseType; timeZone: string; siteId: string }, "name" | "timeZone" | "siteId">
            subject={`warehouse ${row.code}`}
            initial={{ name: row.name, type: row.type, timeZone: row.timeZone, siteId: row.siteId }}
            validate={validateEditZoned}
            fieldCodes={{
              "ORG-WH-SITE-INVALID": { field: "siteId", message: "Choose an active site." },
              "ORG-WH-LE-MISMATCH": { field: "siteId", message: "Choose a site of the same legal entity." },
              "ORG-WH-IS-DEFAULT": { field: "siteId", message: "This is its site's default warehouse; change the site's default first." },
            }}
            save={(change) =>
              save(() =>
                updateWarehouse(row.legalEntityId, row.id, row.version, {
                  name: trimmed(change.name),
                  type: change.type,
                  timeZone: trimmed(change.timeZone),
                  siteId: change.siteId,
                }),
              )
            }
            fields={({ errors, text }) => (
              <>
                <FormField id={`wh-edit-name-${row.id}`} label="Name" error={errors.name}>
                  {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                </FormField>
                <FormField id={`wh-edit-type-${row.id}`} label="Type">
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
                <FormField id={`wh-edit-tz-${row.id}`} label="Time zone" hint="IANA name." error={errors.timeZone}>
                  {(control) => <Input {...control} {...text("timeZone")} />}
                </FormField>
                <FormField
                  id={`wh-edit-site-${row.id}`}
                  label="Site"
                  hint={
                    siteOf(row.siteId)?.defaultWarehouseId === row.id
                      ? "Active sites of the same legal entity. This is its site's default warehouse: change the site's default before moving it."
                      : "Active sites of the same legal entity."
                  }
                  error={errors.siteId}
                >
                  {(control) => (
                    <Select {...control} {...text("siteId")}>
                      {!siteOf(row.siteId) ? <option value={row.siteId}>{row.siteId}</option> : null}
                      {sites.rows
                        .filter((site) => site.legalEntityId === row.legalEntityId && (site.status === "active" || site.id === row.siteId))
                        .map((site) => (
                          <option key={site.id} value={site.id}>
                            {site.code} - {site.name}
                          </option>
                        ))}
                    </Select>
                  )}
                </FormField>
              </>
            )}
          />
        ), canWriteAddress ? (row, save) => (
          <AddressAction
            subject={`warehouse ${row.code}`}
            ownerType="warehouse"
            ownerId={row.id}
            legalEntityId={row.legalEntityId}
            addressId={row.addressId}
            setPointer={(addressId) => save(() => updateWarehouse(row.legalEntityId, row.id, row.version, { addressId }))}
          />
        ) : undefined),
      ]}
      toolbar={({ reload }) =>
        canWriteSite ? (
          <>
            {sites.error ? (
              <Alert tone="danger" title="Could not load the sites" live="alert">
                <ProblemMessage problem={sites.error} />
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

export function LocationsPage() {
  const { canWriteSite, canWriteAddress, scope } = useLegalEntity();
  // Warehouses in the selected scope, for the warehouse column and the form (ALL needs the all-scope duty).
  const warehouses = useRows(
    (signal) => (scope === "" ? Promise.resolve([]) : listWarehouses(scope, { signal }).then((page) => page.items)),
    `warehouses:${scope}`,
  );
  const warehouseOf = (id: string) => warehouses.rows.find((row) => row.id === id);
  return (
    <ScopedListPage<Location>
      title="Locations"
      label="Locations"
      emptyTitle="No locations"
      getRowKey={(row) => row.id}
      load={listLocations}
      columns={(controls) => [
        { key: "code", header: "Code", mono: true },
        { key: "name", header: "Name" },
        { key: "warehouseId", header: "Warehouse", render: (row) => warehouseOf(row.warehouseId)?.code ?? row.warehouseId },
        { key: "type", header: "Type" },
        ...statusColumns(canWriteSite, controls, "location", {
          set: (row, status) => setLocationStatus(row.legalEntityId, row.id, row.version, status),
          get: (row) => getLocation(row.legalEntityId, row.id),
        }, (row, save) => (
          <EditAction<{ name: string; type: LocationType }, "name">
            subject={`location ${row.code}`}
            initial={{ name: row.name, type: row.type }}
            validate={validateEditName}
            save={(change) => save(() => updateLocation(row.legalEntityId, row.id, row.version, { name: trimmed(change.name), type: change.type }))}
            fields={({ errors, text }) => (
              <>
                <FormField id={`loc-edit-name-${row.id}`} label="Name" error={errors.name}>
                  {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                </FormField>
                <FormField id={`loc-edit-type-${row.id}`} label="Type">
                  {(control) => (
                    <Select {...control} {...text("type")}>
                      {locationTypes.map((type) => (
                        <option key={type} value={type}>
                          {label(type)}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
              </>
            )}
          />
        ), canWriteAddress ? (row, save) => (
          <AddressAction
            subject={`location ${row.code}`}
            ownerType="location"
            ownerId={row.id}
            legalEntityId={row.legalEntityId}
            addressId={row.addressId}
            setPointer={(addressId) => save(() => updateLocation(row.legalEntityId, row.id, row.version, { addressId }))}
          />
        ) : undefined),
      ]}
      toolbar={({ reload }) =>
        canWriteSite ? (
          <>
            {warehouses.error ? (
              <Alert tone="danger" title="Could not load the warehouses" live="alert">
                <ProblemMessage problem={warehouses.error} />
              </Alert>
            ) : null}
            <CreateForm<LocationValues, "warehouseId" | "code" | "name">
              title="Add location"
              subject="location"
              initial={{ warehouseId: "", code: "", name: "", type: "storage" }}
              validate={validateLocation}
              send={(values) =>
                createLocation(warehouseOf(values.warehouseId)?.legalEntityId ?? "", {
                  warehouseId: values.warehouseId,
                  code: values.code.toUpperCase(),
                  name: values.name.trim(),
                  type: values.type,
                })
              }
              duplicate={duplicateOf("ORG-LOC-CODE-DUPLICATE", "A location with this code already exists in the warehouse.")}
              onCreated={reload}
              fields={({ errors, text }) => (
                <>
                  <FormField id="loc-warehouse" label="Warehouse" error={errors.warehouseId}>
                    {(control) => (
                      <Select {...control} {...text("warehouseId")}>
                        <option value="">Select a warehouse</option>
                        {warehouses.rows
                          .filter((row) => row.status === "active")
                          .map((row) => (
                            <option key={row.id} value={row.id}>
                              {row.code} - {row.name}
                            </option>
                          ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id="loc-code" label="Code" error={errors.code}>
                    {(control) => <Input {...control} {...text("code")} maxLength={20} />}
                  </FormField>
                  <FormField id="loc-name" label="Name" error={errors.name}>
                    {(control) => <Input {...control} {...text("name")} maxLength={120} />}
                  </FormField>
                  <FormField id="loc-type" label="Type">
                    {(control) => (
                      <Select {...control} {...text("type")}>
                        {locationTypes.map((type) => (
                          <option key={type} value={type}>
                            {label(type)}
                          </option>
                        ))}
                      </Select>
                    )}
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

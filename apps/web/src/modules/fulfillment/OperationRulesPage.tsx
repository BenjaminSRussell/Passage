import { useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listOperationRules,
  POSTING_DATE_SOURCES,
  saveOperationRule,
  WAVE_GROUP_KEYS,
  type OperationRule,
  type PostingDateSource,
  type Problem,
  type WaveGroupBy,
} from "./api/client";
import { dash, formatDateTime, GROUP_LABELS, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Saving a rule needs this duty (the service enforces it); reading needs fulfillment.warehouse.view. */
const DUTY_SAVE = "fulfillment.warehouse.release";

export const POSTING_SOURCE_LABELS: Record<PostingDateSource, string> = {
  SHIP_TIMESTAMP: "Ship timestamp (warehouse time zone)",
  TODAY: "Today (warehouse time zone), changeable",
  MANUAL: "Entered by the poster",
};

const columns: DataTableColumn<OperationRule>[] = [
  { key: "warehouseId", header: "Warehouse", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => row.legalEntityId ?? dash },
  { key: "postingDateSource", header: "Posting date", render: (row) => POSTING_SOURCE_LABELS[row.postingDateSource] },
  { key: "timeZone", header: "Time zone" },
  { key: "autoPackEnabled", header: "Auto-pack", render: (row) => <Badge tone={row.autoPackEnabled ? "neutral" : "warning"}>{row.autoPackEnabled ? "On" : "Off"}</Badge> },
  { key: "workbenchGroupKeyDefault", header: "Workbench groups by", render: (row) => GROUP_LABELS[row.workbenchGroupKeyDefault] },
  { key: "version", header: "Saved", render: (row) => (row.version === 0 ? "Defaults" : row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

interface Values {
  warehouseId: string;
  legalEntityId: string;
  postingDateSource: PostingDateSource;
  timeZone: string;
  autoPackEnabled: boolean;
  workbenchGroupKeyDefault: WaveGroupBy;
}

const blank: Values = { warehouseId: "", legalEntityId: "", postingDateSource: "MANUAL", timeZone: "UTC", autoPackEnabled: true, workbenchGroupKeyDefault: "CUSTOMER" };

/** Whether the browser knows the zone; the service checks the same name against its own database. */
function knownZone(zone: string): boolean {
  try {
    new Intl.DateTimeFormat("en", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

/** Validates the form; the service enforces the same rules (section 5.28.13). */
export function validateOperationRule(values: Values): { errors: Partial<Record<keyof Values, string>>; input?: { warehouseId: string; body: Parameters<typeof saveOperationRule>[2] } } {
  const errors: Partial<Record<keyof Values, string>> = {};
  if (!UUID.test(values.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, or open a warehouse above.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the id of the legal entity the warehouse belongs to.";
  const zone = values.timeZone.trim() === "" ? "UTC" : values.timeZone.trim();
  if (zone.length > 64 || !knownZone(zone)) errors.timeZone = "Enter an IANA time zone such as Europe/Berlin, or UTC.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      warehouseId: values.warehouseId.trim(),
      body: {
        legalEntityId: values.legalEntityId.trim(),
        postingDateSource: values.postingDateSource,
        timeZone: zone,
        autoPackEnabled: values.autoPackEnabled,
        workbenchGroupKeyDefault: values.workbenchGroupKeyDefault,
      },
    },
  };
}

/** Posting and packing rules (BR-28.3, FR-28.15, FR-28.13): where a slip's posting date comes from, auto-pack and the workbench grouping of each warehouse. */
export function OperationRulesPage() {
  const state = useLoad((signal) => listOperationRules(signal), "operation-rules");
  const [values, setValues] = useState<Values>(blank);
  const [version, setVersion] = useState(0);
  const [errors, setErrors] = useState<Partial<Record<keyof Values, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_SAVE);

  function update(change: Partial<Values>) {
    setValues((current) => ({ ...current, ...change }));
    key.renew();
  }

  function open(row: OperationRule) {
    setValues({
      warehouseId: row.warehouseId,
      legalEntityId: row.legalEntityId ?? "",
      postingDateSource: row.postingDateSource,
      timeZone: row.timeZone,
      autoPackEnabled: row.autoPackEnabled,
      workbenchGroupKeyDefault: row.workbenchGroupKeyDefault,
    });
    setVersion(row.version);
    setErrors({});
    setNotice(null);
    setFailure(null);
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const { errors: found, input } = validateOperationRule(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await saveOperationRule(input.warehouseId, version, input.body, key.current());
      key.renew();
      setVersion(saved.version);
      setNotice(`Saved: ${POSTING_SOURCE_LABELS[saved.postingDateSource].toLowerCase()}, auto-pack ${saved.autoPackEnabled ? "on" : "off"}, workbench groups by ${GROUP_LABELS[saved.workbenchGroupKeyDefault].toLowerCase()}.`);
      state.reload();
    } catch (failed) {
      const problem = describeProblem(failed);
      setFailure(
        problem.code === "conflict_version"
          ? { ...problem, message: "Someone else changed this rule. Reload the list and open the warehouse again." }
          : problem.code === "FUL-0057"
            ? { ...problem, message: "This warehouse belongs to another legal entity. Check the legal entity id." }
            : problem,
      );
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Posting and packing rules"
        description="Per warehouse: whether a packing slip's posting date is entered, defaults to today or is always the day of the post (both in the warehouse time zone), whether auto-pack is allowed, and how the picking workbench groups when a call names no group."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the posting and packing rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Posting and packing rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.warehouseId}
          loading={state.loading}
          onRowOpen={canSave ? open : undefined}
          empty={{ title: "No warehouses yet", description: canSave ? "A warehouse appears here once an order is released to it. Until then you can enter its id below." : "A warehouse appears here once an order is released to it." }}
        />
      )}
      {canSave ? (
        <Card title="Change the posting and packing rule">
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="or-warehouse" label="Warehouse id" error={errors.warehouseId}>
              {(control) => <Input {...control} value={values.warehouseId} invalid={Boolean(errors.warehouseId)} onChange={(event) => { const id = event.target.value; update({ warehouseId: id }); setVersion(state.data?.find((r) => r.warehouseId === id.trim())?.version ?? 0); }} />}
            </FormField>
            <FormField id="or-legal-entity" label="Legal entity id" hint="The legal entity the warehouse belongs to; filled in when you open a warehouse above." error={errors.legalEntityId}>
              {(control) => <Input {...control} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(event) => update({ legalEntityId: event.target.value })} />}
            </FormField>
            <FormField id="or-source" label="Posting date of a packing slip" hint="Entered: the poster names the date. Today: the day of the post unless the poster names another. Ship timestamp: always the day of the post; a different date is refused.">
              {(control) => (
                <Select {...control} value={values.postingDateSource} onChange={(event) => update({ postingDateSource: event.target.value as PostingDateSource })}>
                  {POSTING_DATE_SOURCES.map((source) => (
                    <option key={source} value={source}>
                      {POSTING_SOURCE_LABELS[source]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="or-zone" label="Warehouse time zone" hint="An IANA name such as Europe/Berlin. Used for the day of the post; empty is UTC." error={errors.timeZone}>
              {(control) => <Input {...control} value={values.timeZone} invalid={Boolean(errors.timeZone)} onChange={(event) => update({ timeZone: event.target.value })} />}
            </FormField>
            <FormField id="or-autopack" label="Allow auto-pack" hint="Off keeps packing manual: auto-pack is refused for this warehouse.">
              {(control) => <Input {...control} type="checkbox" checked={values.autoPackEnabled} onChange={(event) => update({ autoPackEnabled: event.target.checked })} />}
            </FormField>
            <FormField id="or-group" label="Workbench groups by">
              {(control) => (
                <Select {...control} value={values.workbenchGroupKeyDefault} onChange={(event) => update({ workbenchGroupKeyDefault: event.target.value as WaveGroupBy })}>
                  {WAVE_GROUP_KEYS.map((group) => (
                    <option key={group} value={group}>
                      {GROUP_LABELS[group]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <ProblemAlert title="Could not save the rule" problem={failure} />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Save rule
              </Button>
              {notice ? (
                <p role="status" className={styles.note}>
                  {notice}
                </p>
              ) : null}
            </div>
          </form>
        </Card>
      ) : (
        <p className={styles.note}>Changing a posting and packing rule needs the fulfillment.warehouse.release duty.</p>
      )}
    </div>
  );
}

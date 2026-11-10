import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listReleaseRules, saveReleaseRule, type Problem, type ReleaseRule } from "./api/client";
import { dash, formatDateTime, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Saving a rule needs this duty (the service enforces it); reading needs fulfillment.warehouse.view. */
const DUTY_SAVE = "fulfillment.warehouse.release";

const columns: DataTableColumn<ReleaseRule>[] = [
  { key: "warehouseId", header: "Warehouse", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => row.legalEntityId ?? dash },
  { key: "horizonDays", header: "Horizon (days)", align: "end" },
  { key: "requireFullAtp", header: "ATP", render: (row) => <Badge tone={row.requireFullAtp ? "neutral" : "info"}>{row.requireFullAtp ? "Full ATP only" : "Partial ATP allowed"}</Badge> },
  { key: "waveMaxLines", header: "Wave lines", align: "end" },
  { key: "waveMaxOrders", header: "Wave orders", align: "end" },
  { key: "version", header: "Saved", render: (row) => (row.version === 0 ? "Defaults" : row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

interface Values {
  warehouseId: string;
  legalEntityId: string;
  horizonDays: string;
  requireFullAtp: boolean;
  waveMaxLines: string;
  waveMaxOrders: string;
}

const blank: Values = { warehouseId: "", legalEntityId: "", horizonDays: "2", requireFullAtp: true, waveMaxLines: "200", waveMaxOrders: "50" };

const whole = (text: string) => (/^\d{1,5}$/.test(text.trim()) ? Number(text.trim()) : NaN);

/** Validates the form; the service enforces the same ranges (section 5.28.13). */
export function validateReleaseRule(values: Values): { errors: Partial<Record<keyof Values, string>>; input?: { warehouseId: string; version: number; body: Parameters<typeof saveReleaseRule>[2] } } {
  const errors: Partial<Record<keyof Values, string>> = {};
  if (!UUID.test(values.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, or open a warehouse above.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the id of the legal entity the warehouse belongs to.";
  const horizon = whole(values.horizonDays);
  if (!(horizon >= 0 && horizon <= 30)) errors.horizonDays = "Enter 0 to 30 days.";
  const lines = whole(values.waveMaxLines);
  if (!(lines >= 1 && lines <= 2000)) errors.waveMaxLines = "Enter 1 to 2000 lines.";
  const orders = whole(values.waveMaxOrders);
  if (!(orders >= 1 && orders <= 500)) errors.waveMaxOrders = "Enter 1 to 500 orders.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      warehouseId: values.warehouseId.trim(),
      version: 0,
      body: { legalEntityId: values.legalEntityId.trim(), horizonDays: horizon, requireFullAtp: values.requireFullAtp, waveMaxLines: lines, waveMaxOrders: orders },
    },
  };
}

/** Release rules (FR-28.1, FR-28.13): the release horizon, the ATP rule and the wave limits of each warehouse. */
export function ReleaseRulesPage() {
  const state = useLoad((signal) => listReleaseRules(signal), "release-rules");
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

  function open(row: ReleaseRule) {
    setValues({
      warehouseId: row.warehouseId,
      legalEntityId: row.legalEntityId ?? "",
      horizonDays: String(row.horizonDays),
      requireFullAtp: row.requireFullAtp,
      waveMaxLines: String(row.waveMaxLines),
      waveMaxOrders: String(row.waveMaxOrders),
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
    const { errors: found, input } = validateReleaseRule(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await saveReleaseRule(input.warehouseId, version, input.body, key.current());
      key.renew();
      setVersion(saved.version);
      setNotice(`Saved: release ${saved.horizonDays} days ahead, waves of at most ${saved.waveMaxLines} lines and ${saved.waveMaxOrders} orders.`);
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
        title="Release rules"
        description="Per warehouse: how far ahead a confirmed line is released, whether a line short of ATP waits, and the most lines and orders in a pick wave. A release run or a workbench call that names its own value keeps it."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the release rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Release rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.warehouseId}
          loading={state.loading}
          onRowOpen={canSave ? open : undefined}
          empty={{ title: "No warehouses yet", description: canSave ? "A warehouse appears here once an order is released to it. Until then you can enter its id below." : "A warehouse appears here once an order is released to it." }}
        />
      )}
      {canSave ? (
        <Card title="Change the release rule">
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="rr-warehouse" label="Warehouse" error={errors.warehouseId}>
              {(control) => <EntityLookup entity="warehouse" {...control} value={values.warehouseId} invalid={Boolean(errors.warehouseId)} onChange={(event) => { const id = event.target.value; update({ warehouseId: id }); setVersion(state.data?.find((r) => r.warehouseId === id.trim())?.version ?? 0); }} />}
            </FormField>
            <FormField id="rr-legal-entity" label="Legal entity" hint="The legal entity the warehouse belongs to; filled in when you open a warehouse above." error={errors.legalEntityId}>
              {(control) => <EntityLookup entity="legalEntity" prefill={false} {...control} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(event) => update({ legalEntityId: event.target.value })} />}
            </FormField>
            <FormField id="rr-horizon" label="Release horizon (days)" hint="A line confirmed for a later date than today plus this is not released. 0 to 30." error={errors.horizonDays}>
              {(control) => <Input {...control} inputMode="numeric" value={values.horizonDays} invalid={Boolean(errors.horizonDays)} onChange={(event) => update({ horizonDays: event.target.value })} />}
            </FormField>
            <FormField id="rr-atp" label="Release only lines with full ATP" hint="Off lets a line short of ATP release.">
              {(control) => <Input {...control} type="checkbox" checked={values.requireFullAtp} onChange={(event) => update({ requireFullAtp: event.target.checked })} />}
            </FormField>
            <FormField id="rr-lines" label="Most lines in a wave" hint="1 to 2000." error={errors.waveMaxLines}>
              {(control) => <Input {...control} inputMode="numeric" value={values.waveMaxLines} invalid={Boolean(errors.waveMaxLines)} onChange={(event) => update({ waveMaxLines: event.target.value })} />}
            </FormField>
            <FormField id="rr-orders" label="Most orders in a wave" hint="1 to 500." error={errors.waveMaxOrders}>
              {(control) => <Input {...control} inputMode="numeric" value={values.waveMaxOrders} invalid={Boolean(errors.waveMaxOrders)} onChange={(event) => update({ waveMaxOrders: event.target.value })} />}
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
        <p className={styles.note}>Changing a release rule needs the fulfillment.warehouse.release duty.</p>
      )}
    </div>
  );
}

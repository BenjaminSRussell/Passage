import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listWarehouseSettings,
  saveWarehouseSetting,
  SHORT_PICK_ACTIONS,
  type Problem,
  type ShortPickAction,
  DEFAULT_SHORT_PICK_ACTION,
  type WarehouseSetting,
} from "./api/client";
import { dash, formatDateTime, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export const ACTION_LABELS: Record<ShortPickAction, string> = {
  KEEP_OPEN: "Keep open for a re-pick",
  CANCEL_REMAINDER: "Close the remainder",
  REMAINDER_BACKORDER: "Move the remainder to a backorder shipment",
};

const columns: DataTableColumn<WarehouseSetting>[] = [
  { key: "warehouseId", header: "Warehouse", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => row.legalEntityId ?? dash },
  {
    key: "shortPickAction",
    header: "Short pick action",
    render: (row) => <Badge tone={row.shortPickAction === "REMAINDER_BACKORDER" ? "neutral" : "info"}>{ACTION_LABELS[row.shortPickAction]}</Badge>,
  },
  { key: "updatedAt", header: "Changed", render: (row) => (row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

/** Warehouse settings (FR-28.8): what happens to the open remainder once a packing slip has shipped part of a shipment. */
export function WarehouseSettingsPage() {
  const state = useLoad((signal) => listWarehouseSettings(signal), "warehouse-settings");
  const [warehouseId, setWarehouseId] = useState("");
  const [legalEntityId, setLegalEntityId] = useState("");
  const [action, setAction] = useState<ShortPickAction>(DEFAULT_SHORT_PICK_ACTION);
  const [version, setVersion] = useState(0);
  const [error, setError] = useState<string | undefined>();
  const [legalEntityError, setLegalEntityError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function open(row: WarehouseSetting) {
    setWarehouseId(row.warehouseId);
    setLegalEntityId(row.legalEntityId ?? "");
    setLegalEntityError(undefined);
    setAction(row.shortPickAction);
    setVersion(row.version);
    setNotice(null);
    setFailure(null);
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const badWarehouse = !UUID.test(warehouseId.trim());
    const badLegalEntity = !UUID.test(legalEntityId.trim());
    setError(badWarehouse ? "Enter the warehouse id, or open a warehouse above." : undefined);
    setLegalEntityError(badLegalEntity ? "Enter the id of the legal entity the warehouse belongs to." : undefined);
    if (badWarehouse || badLegalEntity) return;
    setBusy(true);
    try {
      const known = state.data?.find((w) => w.warehouseId === warehouseId.trim());
      const saved = await saveWarehouseSetting(
        warehouseId.trim(),
        known ? known.version : version,
        { shortPickAction: action, legalEntityId: legalEntityId.trim() },
        key.current(),
      );
      key.renew();
      setVersion(saved.version);
      setNotice(`Saved: ${ACTION_LABELS[saved.shortPickAction].toLowerCase()}.`);
      state.reload();
    } catch (failed) {
      const problem = describeProblem(failed);
      setFailure(
        problem.code === "conflict_version"
          ? { ...problem, message: "Someone else changed this setting. Reload the list and open the warehouse again." }
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
        title="Warehouse settings"
        description="What happens to the open remainder once a packing slip has shipped part of a shipment: keep it open for a re-pick, close it, or move it to a backorder shipment."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the warehouse settings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Warehouse settings"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.warehouseId}
          loading={state.loading}
          onRowOpen={open}
          empty={{ title: "No warehouses yet", description: "A warehouse appears here once an order is released to it. Until then you can enter its id below." }}
        />
      )}
      <Card title="Change the short pick action">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>The action runs when a packing slip posts and leaves quantity still to ship. A shipment with packed stock waiting is left as it is.</p>
          <FormField id="ws-warehouse" label="Warehouse" error={error}>
            {(control) => <EntityLookup entity="warehouse" {...control} value={warehouseId} invalid={Boolean(error)} onChange={(event) => { setWarehouseId(event.target.value); setVersion(0); key.renew(); }} />}
          </FormField>
          <FormField id="ws-legal-entity" label="Legal entity" hint="The legal entity the warehouse belongs to; filled in when you open a warehouse above." error={legalEntityError}>
            {(control) => <EntityLookup entity="legalEntity" prefill={false} {...control} value={legalEntityId} invalid={Boolean(legalEntityError)} onChange={(event) => { setLegalEntityId(event.target.value); key.renew(); }} />}
          </FormField>
          <FormField id="ws-action" label="Short pick action">
            {(control) => (
              <Select {...control} value={action} onChange={(event) => { setAction(event.target.value as ShortPickAction); key.renew(); }}>
                {SHORT_PICK_ACTIONS.map((a) => (
                  <option key={a} value={a}>
                    {ACTION_LABELS[a]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <ProblemAlert title="Could not save the setting" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save setting
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}

import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader } from "../../shell/ui";
import { buildLoads, describeProblem, LOAD_MODES, newKey, type Load, type LoadBuildRequest, type LoadMode, type Problem } from "./api/client";
import { AMOUNT, DATE, UUID } from "./format";
import { loadColumns } from "./LoadsPage";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { ProblemMessage } from "../../shell/errors";

export interface BuildValues {
  maxWeightKg: string;
  maxVolumeM3: string;
  maxPallets: string;
  shipments: Record<string, string>[];
}

const shipmentFields: RowField[] = [
  { key: "id", label: "Shipment id", mono: true, hint: "Sales, transfer or inbound shipment (UUID)." },
  { key: "shipFromSiteId", label: "Ship-from site id", mono: true },
  { key: "shipToRegion", label: "Ship-to region", upper: true, hint: "Such as US-SE." },
  { key: "mode", label: "Mode", options: LOAD_MODES },
  { key: "pickupDate", label: "Pickup date", hint: "YYYY-MM-DD" },
  { key: "weightKg", label: "Weight kg", decimal: true },
  { key: "volumeM3", label: "Volume m3", decimal: true },
  { key: "pallets", label: "Pallets" },
];

const blankShipment = () => ({ id: "", shipFromSiteId: "", shipToRegion: "", mode: "TL", pickupDate: "", weightKg: "", volumeM3: "0", pallets: "0" });

const wholeNumber = /^\d{1,6}$/;

/** Checks that mirror ValidateLoadBuild in the service, and the request the form sends. */
export function validateBuild(v: BuildValues): { errors: Record<string, string>; input?: LoadBuildRequest } {
  const errors: Record<string, string> = {};
  const positive = (value: string) => AMOUNT.test(value) && !/^0+(\.0+)?$/.test(value);
  if (!positive(v.maxWeightKg.trim())) errors.maxWeightKg = "Enter the maximum weight in kg, greater than zero.";
  if (!positive(v.maxVolumeM3.trim())) errors.maxVolumeM3 = "Enter the maximum volume in m3, greater than zero.";
  if (!wholeNumber.test(v.maxPallets.trim()) || Number(v.maxPallets) < 1) errors.maxPallets = "Enter the pallet positions as a whole number of at least 1.";
  const seen = new Set<string>();
  const shipments = v.shipments.map((s, i) => {
    const row = `shipments[${i}]`;
    const id = s.id.trim();
    const region = s.shipToRegion.trim().toUpperCase();
    if (!UUID.test(id)) errors[row] = "Enter the shipment id as a lower-case UUID.";
    else if (seen.has(id)) errors[row] = "List each shipment once.";
    else if (!UUID.test(s.shipFromSiteId.trim())) errors[row] = "Enter the ship-from site id as a lower-case UUID.";
    else if (region.length < 1 || region.length > 20) errors[row] = "Enter a ship-to region of 1 to 20 characters.";
    else if (!DATE.test(s.pickupDate.trim())) errors[row] = "Enter the pickup date as YYYY-MM-DD.";
    else if (!AMOUNT.test(s.weightKg.trim())) errors[row] = "Enter the weight in kg, such as 1600.";
    else if (!AMOUNT.test(s.volumeM3.trim())) errors[row] = "Enter the volume in m3, such as 2.5.";
    else if (!wholeNumber.test(s.pallets.trim())) errors[row] = "Enter the pallets as a whole number.";
    seen.add(id);
    return {
      id,
      shipFromSiteId: s.shipFromSiteId.trim(),
      shipToRegion: region,
      mode: s.mode as LoadMode,
      pickupDate: s.pickupDate.trim(),
      weightKg: s.weightKg.trim(),
      volumeM3: s.volumeM3.trim(),
      pallets: Number(s.pallets),
    };
  });
  if (shipments.length === 0 || shipments.length > 500) errors.shipments = "Add between 1 and 500 shipments.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { capacity: { maxWeightKg: v.maxWeightKg.trim(), maxVolumeM3: v.maxVolumeM3.trim(), maxPallets: Number(v.maxPallets) }, shipments },
  };
}

/** Consolidate shipments into loads by rule (FR-34.1). A load never goes over capacity: what does not fit opens another load (BR-34.8). */
export function BuildLoadsPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BuildValues>({ maxWeightKg: "20000", maxVolumeM3: "90", maxPallets: "26", shipments: [blankShipment()] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [built, setBuilt] = useState<Load[] | null>(null);
  const key = useRef(newKey());

  // The service names capacity fields capacity.maxWeightKg and so on.
  const capacityError = (field: string) => errors[field] ?? errors[`capacity.${field}`];

  function update(patch: Partial<BuildValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBuild(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setBuilt(await buildLoads(input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (built) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Build loads" />
        <Alert tone="success" title={`${built.length} ${built.length === 1 ? "load" : "loads"} planned`} live="status">
          Every shipment is on exactly one load and no load is over its capacity.
        </Alert>
        <DataTable label="Loads built" columns={loadColumns} rows={built} getRowKey={(row) => row.id} onRowOpen={(row) => navigate(`/transport/loads/${row.id}`)} />
        <div className={styles.actions}>
          <Link className={buttonClassName("primary")} to="/transport/loads">
            All loads
          </Link>
          <Button
            onClick={() => {
              setBuilt(null);
              setValues((v) => ({ ...v, shipments: [blankShipment()] }));
              key.current = newKey();
            }}
          >
            Build more loads
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Build loads"
        description="Shipments with the same ship-from site, ship-to region, mode and pickup date share loads, heaviest first. A shipment larger than an empty load is refused (TMS-0115)."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="build-weight" label="Maximum weight (kg)" hint="Per load, such as 20000 for a TL." error={capacityError("maxWeightKg")}>
            {(control) => <Input {...control} inputMode="decimal" value={values.maxWeightKg} onChange={(e) => update({ maxWeightKg: e.target.value })} invalid={Boolean(capacityError("maxWeightKg"))} />}
          </FormField>
          <FormField id="build-volume" label="Maximum volume (m3)" error={capacityError("maxVolumeM3")}>
            {(control) => <Input {...control} inputMode="decimal" value={values.maxVolumeM3} onChange={(e) => update({ maxVolumeM3: e.target.value })} invalid={Boolean(capacityError("maxVolumeM3"))} />}
          </FormField>
          <FormField id="build-pallets" label="Pallet positions" error={capacityError("maxPallets")}>
            {(control) => <Input {...control} inputMode="numeric" value={values.maxPallets} onChange={(e) => update({ maxPallets: e.target.value })} invalid={Boolean(capacityError("maxPallets"))} />}
          </FormField>
          <RowsEditor
            field="shipments"
            legend="Shipments to plan"
            noun="shipment"
            fields={shipmentFields}
            rows={values.shipments}
            errors={errors}
            blank={blankShipment}
            onChange={(shipments) => update({ shipments })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not build the loads" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Build loads
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/loads">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

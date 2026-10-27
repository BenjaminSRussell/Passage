import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listRateCards, type RateCard } from "./api/client";
import { dash, fixed, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<RateCard>[] = [
  { key: "carrierId", header: "Carrier", mono: true, render: (row) => <span title={row.carrierId}>{shortId(row.carrierId)}</span> },
  { key: "serviceId", header: "Service", mono: true, render: (row) => <span title={row.serviceId}>{shortId(row.serviceId)}</span> },
  { key: "basis", header: "Basis", render: (row) => <Badge tone="neutral">{row.basis}</Badge> },
  { key: "currency", header: "Currency", mono: true },
  { key: "validFrom", header: "Valid from", mono: true },
  { key: "validTo", header: "Valid to", mono: true, render: (row) => row.validTo ?? "Open" },
  { key: "minimumCharge", header: "Minimum", align: "end", render: (row) => fixed(row.minimumCharge, 2) },
  { key: "fuelPct", header: "Fuel %", align: "end", render: (row) => (row.fuelPct === "0" ? dash : fixed(row.fuelPct, 2)) },
  { key: "rows", header: "Rate rows", align: "end", render: (row) => String(row.rows.length) },
];

/** Carrier rate cards (BR-34.2, BR-34.10): one effective-dated card per carrier service and basis at a time. */
export function RateCardsPage() {
  const [carrier, setCarrier] = useState("");
  const [applied, setApplied] = useState("");
  const invalid = carrier.trim() !== "" && !UUID.test(carrier.trim());
  const state = useLoad((signal) => listRateCards({ carrierId: applied }, signal), `rate-cards:${applied}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rate cards"
        description="Effective-dated price lists per carrier service. Cards of the same carrier, service and basis may not share a day; rate shopping uses the card valid on the ship date."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/rate-cards/new">
            New rate card
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid) setApplied(carrier.trim());
        }}
      >
        <FormField id="rate-card-carrier" label="Carrier id" optional error={invalid ? "Enter the carrier id as a lower-case UUID." : undefined}>
          {(control) => <Input {...control} className={styles.mono} value={carrier} onChange={(e) => setCarrier(e.target.value)} invalid={invalid} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load rate cards" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Rate cards"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No rate cards yet", description: "Add a carrier's rate card before rate shopping its services." }}
        />
      )}
    </div>
  );
}

import { useNavigate } from "react-router-dom";
import { Alert, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPacks, type Pack } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Pack>[] = [
  { key: "code", header: "Pack", mono: true },
  { key: "name", header: "Country" },
  { key: "version", header: "Version", mono: true },
  { key: "currency", header: "Currency", mono: true },
  { key: "taxRegime", header: "Tax regime" },
  { key: "bankFormats", header: "Bank formats", render: (p) => p.bankFormats.join(", ") },
  { key: "reports", header: "Statutory reports", align: "end", render: (p) => p.statutoryReports.length },
];

/** Country packs (PRD §9.48.6, localization.admin): the seven first packs and what each one ships. */
export function PacksPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listPacks(signal), "packs");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Country packs"
        description="Tax, statutory reports, document layouts, bank formats and calendars per country. Open a pack to see its contents and check whether it can be installed on a legal entity."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the country packs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Country packs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(p) => p.code}
          loading={state.loading}
          onRowOpen={(p) => navigate(`/compliance/packs/${p.code}`)}
          empty={{ title: "No country packs loaded", description: "The localization service loads packs from its countrypacks directory at start-up." }}
        />
      )}
    </div>
  );
}

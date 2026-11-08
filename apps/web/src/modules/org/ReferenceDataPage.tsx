import { Fragment, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  listCountries,
  listCurrencies,
  listLanguages,
  listRegions,
  setReferenceStatus,
  type Country,
  type Currency,
  type Language,
  type ReferenceKind,
  type ReferenceStatus,
  type Region,
} from "./api/reference";
import styles from "./pages.module.css";
import { FilterSelect, StatusTable, statusOptions, useRows, type StatusFilterValue } from "./referenceParts";
import { RoundingTab } from "./RoundingTab";
import { ConversionsTab, UnitsTab } from "./UomTabs";

interface ReferenceListTabProps<T extends { code: string; status: ReferenceStatus }> {
  label: string;
  kind: ReferenceKind;
  columns: DataTableColumn<T>[];
  getRowKey(row: T): string;
  load(status: ReferenceStatus | undefined, signal: AbortSignal): Promise<T[]>;
}

function ReferenceListTab<T extends { code: string; status: ReferenceStatus }>({
  label,
  kind,
  columns,
  getRowKey,
  load,
}: ReferenceListTabProps<T>) {
  const [status, setStatus] = useState<StatusFilterValue>("");
  const state = useRows((signal) => load(status || undefined, signal), status);
  return (
    <>
      <div className={styles.filters}>
        <FilterSelect id={`${kind}-status`} label="Filter by status" value={status} options={statusOptions} onChange={(value) => setStatus(value as StatusFilterValue)} />
      </div>
      <StatusTable
        label={label}
        emptyTitle={`No ${label.toLowerCase()}`}
        columns={columns}
        state={state}
        getRowKey={getRowKey}
        action={{
          subject: (row) => row.code,
          apply: (row, next) => setReferenceStatus<Partial<T>>(kind, row.code, next),
        }}
      />
    </>
  );
}

const codeAndName = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
];

const countryColumns: DataTableColumn<Country>[] = codeAndName;
const languageColumns: DataTableColumn<Language>[] = codeAndName;
const currencyColumns: DataTableColumn<Currency>[] = [
  ...codeAndName,
  { key: "numericCode", header: "Numeric code", mono: true },
  { key: "minorUnits", header: "Minor units", align: "end" },
];
const regionColumns: DataTableColumn<Region>[] = [...codeAndName, { key: "countryCode", header: "Country" }];

const tabs: { id: string; label: string; render(): ReactNode }[] = [
  {
    id: "countries",
    label: "Countries",
    render: () => (
      <ReferenceListTab label="Countries" kind="countries" columns={countryColumns} getRowKey={(row) => row.code} load={(status, signal) => listCountries(status, { signal })} />
    ),
  },
  {
    id: "currencies",
    label: "Currencies",
    render: () => (
      <ReferenceListTab label="Currencies" kind="currencies" columns={currencyColumns} getRowKey={(row) => row.code} load={(status, signal) => listCurrencies(status, { signal })} />
    ),
  },
  {
    id: "regions",
    label: "Regions",
    render: () => (
      <ReferenceListTab label="Regions" kind="regions" columns={regionColumns} getRowKey={(row) => `${row.countryCode}:${row.code}`} load={(status, signal) => listRegions(status, { signal })} />
    ),
  },
  {
    id: "languages",
    label: "Languages",
    render: () => (
      <ReferenceListTab label="Languages" kind="languages" columns={languageColumns} getRowKey={(row) => row.code} load={(status, signal) => listLanguages(status, { signal })} />
    ),
  },
  { id: "units", label: "Units", render: () => <UnitsTab /> },
  { id: "conversions", label: "Conversions", render: () => <ConversionsTab /> },
  { id: "rounding", label: "Rounding", render: () => <RoundingTab /> },
];

export function ReferenceDataPage() {
  const [active, setActive] = useState(0);
  const buttons = useRef<(HTMLButtonElement | null)[]>([]);

  const move = (event: KeyboardEvent) => {
    const last = tabs.length - 1;
    const target =
      event.key === "ArrowRight" ? (active === last ? 0 : active + 1)
      : event.key === "ArrowLeft" ? (active === 0 ? last : active - 1)
      : event.key === "Home" ? 0
      : event.key === "End" ? last
      : null;
    if (target !== null) {
      event.preventDefault();
      setActive(target);
      buttons.current[target]?.focus();
    }
  };

  return (
    <>
      <PageHeader title="Reference data" description="Countries, currencies, regions, languages, units of measure and currency rounding rules for this tenant." />
      <div role="tablist" aria-label="Reference data" className={styles.tablist} onKeyDown={move}>
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            ref={(element) => {
              buttons.current[index] = element;
            }}
            type="button"
            role="tab"
            id={`reference-tab-${tab.id}`}
            aria-selected={index === active}
            aria-controls="reference-panel"
            tabIndex={index === active ? 0 : -1}
            className={styles.tab}
            onClick={() => setActive(index)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id="reference-panel" aria-labelledby={`reference-tab-${tabs[active].id}`} className={styles.panel}>
        <Fragment key={tabs[active].id}>{tabs[active].render()}</Fragment>
      </div>
    </>
  );
}

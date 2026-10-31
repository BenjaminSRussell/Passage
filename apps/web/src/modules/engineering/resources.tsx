import { Fragment, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ApiError,
  getCalendarDays,
  getResource,
  listCapabilities,
  listResourceGroups,
  listResources,
  type CalendarDay,
  type Capability,
  type Page,
  type Resource,
  type ResourceCapability,
  type ResourceFilters,
  type ResourceGroup,
  type ResourceGroupFilters,
} from "./api/client";
import { errorText } from "./screens";
import styles from "./pages.module.css";

const resourceTypes = ["machine", "tool", "labor", "vendor", "location"];
const notFoundStatus = 404;

export function usePagedList<T, F>(fetchPage: (filters: F, cursor?: string) => Promise<Page<T>>, filters: F) {
  const [rows, setRows] = useState<T[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    let current = true;
    generation.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError("");
    fetchPage(filters)
      .then((page) => {
        if (!current) return;
        setRows(page.items);
        setNextCursor(page.nextCursor ?? "");
      })
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setNextCursor("");
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [fetchPage, filters, attempt]);

  async function loadMore() {
    const token = generation.current;
    setLoadingMore(true);
    setError("");
    try {
      const page = await fetchPage(filters, nextCursor);
      if (token !== generation.current) return;
      setRows((existing) => [...existing, ...page.items]);
      setNextCursor(page.nextCursor ?? "");
    } catch (e) {
      if (token === generation.current) setError(errorText(e));
    } finally {
      if (token === generation.current) setLoadingMore(false);
    }
  }

  return { rows, nextCursor, loading, loadingMore, error, loadMore, retry: () => setAttempt((n) => n + 1) };
}

export function PagedTable<T, F>({
  label,
  emptyTitle,
  columns,
  getRowKey,
  fetchPage,
  filters,
  onRowOpen,
}: {
  label: string;
  emptyTitle: string;
  columns: DataTableColumn<T>[];
  getRowKey: (row: T) => string;
  fetchPage: (filters: F, cursor?: string) => Promise<Page<T>>;
  filters: F;
  onRowOpen?: (row: T) => void;
}) {
  const list = usePagedList(fetchPage, filters);
  return (
    <>
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={list.error ? <Button size="sm" onClick={list.retry}>Retry</Button> : undefined}
        >
          {list.error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {list.error && list.rows.length === 0 ? null : (
          <DataTable
            label={label}
            columns={columns}
            rows={list.rows}
            loading={list.loading}
            getRowKey={getRowKey}
            empty={{ title: emptyTitle }}
            onRowOpen={onRowOpen}
          />
        )}
      </div>
      {list.nextCursor !== "" && !list.loading ? (
        <div className={styles.toolbar}>
          <Button loading={list.loadingMore} onClick={list.loadMore}>Load more</Button>
        </div>
      ) : null}
    </>
  );
}

const resourceColumns: DataTableColumn<Resource>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "type", header: "Type" },
  { key: "siteId", header: "Site" },
  { key: "groupId", header: "Group", mono: true },
  { key: "exclusive", header: "Exclusive", render: (row) => (row.exclusive ? "Yes" : "No") },
];

const textFilters = [
  { key: "siteId", id: "resource-site", label: "Site" },
  { key: "groupId", id: "resource-group", label: "Group" },
  { key: "capabilityId", id: "resource-capability", label: "Capability" },
] as const;

export function ResourcesPage({ initial = {}, onOpen }: { initial?: ResourceFilters; onOpen?: (id: string) => void }) {
  const [draft, setDraft] = useState<ResourceFilters>(initial);
  const [applied, setApplied] = useState<ResourceFilters>(initial);

  function changeType(type: string) {
    setDraft((existing) => ({ ...existing, type }));
    setApplied((existing) => ({ ...existing, type }));
  }

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setApplied((existing) => ({ ...draft, type: existing.type }));
  }

  return (
    <>
      <PageHeader title="Resources" description="Machines, tools, labor, vendors and locations." />
      <Card>
        <form className={styles.form} onSubmit={applyFilters}>
          <div className={styles.field}>
            <FormField id="resource-type" label="Type" optional>
              {(control) => (
                <Select {...control} value={draft.type ?? ""} onChange={(e) => changeType(e.target.value)}>
                  <option value="">Any type</option>
                  {resourceTypes.map((type) => (
                    <option key={type} value={type}>
                      {type}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          {textFilters.map(({ key, id, label }) => (
            <div key={key} className={styles.field}>
              <FormField id={id} label={label} optional>
                {(control) => <Input {...control} value={draft[key] ?? ""} onChange={(e) => setDraft({ ...draft, [key]: e.target.value })} />}
              </FormField>
            </div>
          ))}
          <Button type="submit" variant="primary">Apply filters</Button>
        </form>
      </Card>
      <PagedTable
        label="Resources"
        emptyTitle="No resources match"
        columns={resourceColumns}
        getRowKey={(row: Resource) => row.id}
        fetchPage={listResources}
        filters={applied}
        onRowOpen={(row) => onOpen?.(row.id)}
      />
    </>
  );
}

const capabilityColumns: DataTableColumn<ResourceCapability>[] = [
  { key: "capabilityId", header: "Capability", mono: true },
  { key: "level", header: "Level", align: "end" },
  { key: "effectiveFrom", header: "Effective from" },
  { key: "effectiveTo", header: "Effective to" },
];

export function calendarLink(calendarId?: string): ReactNode {
  return calendarId ? <Link to={`/engineering/calendars/${encodeURIComponent(calendarId)}/days`}>Calendar days</Link> : "";
}

export function ResourceDetailPage({ id, onBack }: { id: string; onBack?: () => void }) {
  const [resource, setResource] = useState<Resource | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setResource(null);
    setError(null);
    getResource(id)
      .then((loaded) => current && setResource(loaded))
      .catch((e: Error) => current && setError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  const notFound = error instanceof ApiError && error.status === notFoundStatus;
  const facts: [string, ReactNode][] = resource
    ? [
        ["Code", resource.code],
        ["Name", resource.name],
        ["Type", resource.type],
        ["Site", resource.siteId],
        ["Efficiency %", resource.efficiencyPct],
        ["Capacity per day (h)", resource.capacityPerDayH],
        ["Vendor party", resource.vendorPartyId],
        ["Exclusive", resource.exclusive ? "Yes" : "No"],
        [
          "Group",
          resource.groupId ? <Link to={`/engineering/resources?groupId=${encodeURIComponent(resource.groupId)}`}>{resource.groupId}</Link> : "",
        ],
        ["Effective calendar", calendarLink(resource.effectiveCalendarId)],
      ]
    : [];

  return (
    <>
      <PageHeader title={resource ? `Resource ${resource.code}` : "Resource"} description="Capabilities, group and calendar." />
      <div className={styles.toolbar}>
        {onBack ? <Button size="sm" onClick={onBack}>Back to resources</Button> : null}
      </div>
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={error && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}
        >
          {error ? (notFound ? "This resource does not exist" : errorText(error)) : null}
        </Alert>
      </div>
      {!resource && !error ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {resource ? (
        <>
          <Card title="Resource">
            <dl className={styles.facts}>
              {facts.map(([term, value]) => (
                <Fragment key={term}>
                  <dt>{term}</dt>
                  <dd>{value}</dd>
                </Fragment>
              ))}
            </dl>
          </Card>
          <div className={styles.results}>
            <DataTable
              label="Capabilities"
              columns={capabilityColumns}
              rows={resource.capabilities ?? []}
              getRowKey={(row) => `${row.capabilityId}-${row.effectiveFrom}`}
              empty={{ title: "No capabilities" }}
            />
          </div>
        </>
      ) : null}
    </>
  );
}

const groupColumns: DataTableColumn<ResourceGroup>[] = [
  {
    key: "code",
    header: "Code",
    mono: true,
    render: (row) => <Link to={`/engineering/resource-groups/${encodeURIComponent(row.id)}`}>{row.code}</Link>,
  },
  { key: "name", header: "Name" },
  { key: "siteId", header: "Site" },
  { key: "calendarId", header: "Calendar", render: (row) => calendarLink(row.calendarId) },
];

export function ResourceGroupsPage() {
  const [draft, setDraft] = useState("");
  const [applied, setApplied] = useState<ResourceGroupFilters>({});

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setApplied({ siteId: draft });
  }

  return (
    <>
      <PageHeader title="Resource groups" description="Groups of resources by site." />
      <Card>
        <form className={styles.form} onSubmit={applyFilters}>
          <div className={styles.field}>
            <FormField id="group-site" label="Site" optional>
              {(control) => <Input {...control} value={draft} onChange={(e) => setDraft(e.target.value)} />}
            </FormField>
          </div>
          <Button type="submit" variant="primary">Apply filters</Button>
        </form>
      </Card>
      <PagedTable
        label="Resource groups"
        emptyTitle="No resource groups match"
        columns={groupColumns}
        getRowKey={(row: ResourceGroup) => row.id}
        fetchPage={listResourceGroups}
        filters={applied}
      />
    </>
  );
}

const capabilityListColumns: DataTableColumn<Capability>[] = [
  {
    key: "code",
    header: "Code",
    mono: true,
    render: (row) => <Link to={`/engineering/capabilities/${encodeURIComponent(row.id)}`}>{row.code}</Link>,
  },
  { key: "name", header: "Name" },
];

const noFilters = {};
const fetchCapabilities = (_filters: object, cursor?: string) => listCapabilities(cursor);

export function CapabilitiesPage() {
  return (
    <>
      <PageHeader title="Capabilities" description="Skills and abilities resources can hold." />
      <PagedTable
        label="Capabilities"
        emptyTitle="No capabilities yet"
        columns={capabilityListColumns}
        getRowKey={(row: Capability) => row.id}
        fetchPage={fetchCapabilities}
        filters={noFilters}
      />
    </>
  );
}

interface DayRange {
  id: string;
  from: string;
  to: string;
}

const pad = (n: number) => String(n).padStart(2, "0");
const isoDate = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

export function currentMonth(now = new Date()): { from: string; to: string } {
  return {
    from: isoDate(new Date(now.getFullYear(), now.getMonth(), 1)),
    to: isoDate(new Date(now.getFullYear(), now.getMonth() + 1, 0)),
  };
}

const dayColumns: DataTableColumn<CalendarDay>[] = [
  { key: "date", header: "Date" },
  { key: "open", header: "Open", render: (row) => (row.open ? "Open" : "Closed") },
  { key: "shifts", header: "Shifts", render: (row) => row.shifts.map((s) => `${s.start}-${s.end}`).join(", ") },
  { key: "totalH", header: "Total hours", align: "end" },
];

const fetchDays = ({ id, from, to }: DayRange) => getCalendarDays(id, from, to);

export function CalendarDaysPage({ id, initialRange = currentMonth() }: { id: string; initialRange?: { from: string; to: string } }) {
  const [range, setRange] = useState(initialRange);
  const filters = useMemo<DayRange>(() => ({ id, ...range }), [id, range]);

  return (
    <>
      <PageHeader title="Calendar days" description="Open and closed days with their shifts." />
      <Card>
        <form className={styles.form} onSubmit={(event) => event.preventDefault()}>
          <div className={styles.field}>
            <FormField id="calendar-from" label="From">
              {(control) => <Input {...control} type="date" value={range.from} onChange={(e) => setRange({ ...range, from: e.target.value })} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="calendar-to" label="To">
              {(control) => <Input {...control} type="date" value={range.to} onChange={(e) => setRange({ ...range, to: e.target.value })} />}
            </FormField>
          </div>
        </form>
      </Card>
      <PagedTable
        label="Calendar days"
        emptyTitle="No generated days in this range"
        columns={dayColumns}
        getRowKey={(row: CalendarDay) => row.date}
        fetchPage={fetchDays}
        filters={filters}
      />
    </>
  );
}

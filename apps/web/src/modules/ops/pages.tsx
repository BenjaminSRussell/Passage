import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Card, DataTable, FormField, PageHeader, Select, StatGroup, StatTile, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { fetchBreakGlass, fetchLicenses, fetchRegions, fetchSlo, SLO_WINDOWS, type BreakGlass, type LicenseItem, type Region, type SloLine, type SloWindow } from "./api";
import styles from "./pages.module.css";

// The operations console (#50): failover status, break-glass windows, the SLO report and tenant licences. Reads only; the
// failover itself runs through the passage-ops command line, which records every step back here.

const UNKNOWN_LAG = 1 << 30;

function when(iso: string | null | undefined): string {
  if (!iso) return "Not yet";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function roleTone(role: Region["role"]): BadgeTone {
  return role === "PRIMARY" ? "success" : role === "FENCED" ? "danger" : "neutral";
}

const ROLE_LABEL: Record<Region["role"], string> = { PRIMARY: "Primary", PASSIVE: "Standby", FENCED: "Fenced" };

function lagText(r: Region): string {
  if (r.lagStale || r.lagSeconds >= UNKNOWN_LAG) return "Unknown (no recent report)";
  return `${r.lagSeconds} s`;
}

export function OpsHome() {
  const result = useQuery({ queryKey: ["ops", "regions"], queryFn: ({ signal }) => fetchRegions(signal) });
  const columns: DataTableColumn<Region>[] = [
    { key: "name", header: "Region", mono: true },
    { key: "role", header: "Role", render: (r) => <Badge tone={roleTone(r.role)}>{ROLE_LABEL[r.role]}</Badge> },
    {
      key: "lag",
      header: "Replication lag",
      align: "end",
      render: (r) => (r.role === "PRIMARY" ? "Not replicating (primary)" : lagText(r)),
    },
    {
      key: "ready",
      header: "Can take over",
      render: (r) =>
        r.role !== "PASSIVE" ? "No" : r.lagStale || r.lagSeconds > (result.data?.maxLagSeconds ?? 0) ? "Only by accepting data loss" : "Yes",
    },
  ];
  const items = result.data?.items ?? [];
  const primary = items.find((r) => r.role === "PRIMARY");
  const fenced = items.filter((r) => r.role === "FENCED").length;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Operations console"
        description="Where the platform runs: which region is primary, how far each standby is behind, and whether a failover is possible without losing data."
      />
      {result.error ? <ProblemMessage problem={result.error} fallback="The regions could not be loaded." /> : null}
      <StatGroup label="Failover status">
        <StatTile label="Primary region" value={primary?.name ?? "None"} tone={primary ? "neutral" : "danger"} />
        <StatTile label="Fenced regions" value={String(fenced)} tone={fenced > 0 ? "warning" : "neutral"} hint="A fenced region serves no requests until it rejoins." />
        <StatTile label="Lag limit for a failover" value={result.data ? `${result.data.maxLagSeconds} s` : "…"} />
      </StatGroup>
      <Card title="Regions">
        <DataTable
          label="Regions"
          columns={columns}
          rows={items}
          getRowKey={(r) => r.name}
          loading={result.isLoading}
          empty={{ title: "No regions are registered", description: "The ops service registers the regions from its configuration when it first starts." }}
        />
      </Card>
    </div>
  );
}

const REASON_LABEL: Record<string, string> = {
  INCIDENT_RESPONSE: "Incident response",
  PRODUCTION_OUTAGE: "Production outage",
  REGION_LOSS: "Region loss",
  APPROVER_UNAVAILABLE: "Approver unavailable",
  AUDIT_EVIDENCE_REQUEST: "Audit evidence request",
};

function windowState(g: BreakGlass, now: number): { label: string; tone: BadgeTone } {
  if (g.reviewOutcome === "FLAGGED") return { label: "Flagged in review", tone: "danger" };
  if (g.reviewOutcome === "APPROVED") return { label: "Reviewed", tone: "success" };
  const ended = g.revokedAt ? Date.parse(g.revokedAt) : Date.parse(g.expiresAt);
  if (ended > now) return { label: "Open", tone: "warning" };
  if (Date.parse(g.reviewDueAt) < now) return { label: "Review overdue", tone: "danger" };
  return { label: "Waiting for review", tone: "info" };
}

export function BreakGlassPage() {
  const result = useQuery({ queryKey: ["ops", "break-glass"], queryFn: ({ signal }) => fetchBreakGlass(signal) });
  const now = Date.now();
  const columns: DataTableColumn<BreakGlass>[] = [
    { key: "user", header: "Operator", render: (g) => <EntityName entity="user" id={g.userId} fallback="Operator" /> },
    { key: "reason", header: "Reason", render: (g) => REASON_LABEL[g.reasonCode] ?? g.reasonCode },
    { key: "ticket", header: "Ticket", mono: true, render: (g) => g.ticketRef },
    { key: "granted", header: "Opened", render: (g) => when(g.grantedAt) },
    { key: "ends", header: "Ends", render: (g) => (g.revokedAt ? `Ended early ${when(g.revokedAt)}` : when(g.expiresAt)) },
    { key: "uses", header: "Uses", align: "end", render: (g) => String(g.uses) },
    {
      key: "state",
      header: "State",
      render: (g) => {
        const s = windowState(g, now);
        return <Badge tone={s.tone}>{s.label}</Badge>;
      },
    },
    { key: "reviewer", header: "Reviewed by", render: (g) => (g.reviewedBy ? <EntityName entity="user" id={g.reviewedBy} fallback="Operator" /> : "Not yet") },
  ];
  const items = result.data ?? [];
  const open = items.filter((g) => windowState(g, now).label === "Open").length;
  const overdue = items.filter((g) => windowState(g, now).label === "Review overdue").length;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Break-glass windows"
        description="Time-boxed emergency access to run a failover. Every use is logged, and another operator reviews each window within 3 days after it ends."
      />
      {result.error ? <ProblemMessage problem={result.error} fallback="The break-glass windows could not be loaded." /> : null}
      <StatGroup label="Break-glass summary">
        <StatTile label="Open now" value={String(open)} tone={open > 0 ? "warning" : "neutral"} />
        <StatTile label="Reviews overdue" value={String(overdue)} tone={overdue > 0 ? "danger" : "neutral"} />
      </StatGroup>
      <Card title="Windows">
        <DataTable
          label="Break-glass windows"
          columns={columns}
          rows={items}
          getRowKey={(g) => g.id}
          loading={result.isLoading}
          empty={{ title: "No break-glass windows", description: "Operators open one with passage-ops break-glass open when an approver can't be reached." }}
        />
      </Card>
    </div>
  );
}

function percent(v: number | null): string {
  return v === null ? "No traffic" : `${(v * 100).toFixed(3)} %`;
}

const WINDOW_LABEL: Record<SloWindow, string> = { "1h": "Last hour", "6h": "Last 6 hours", "24h": "Last 24 hours", "7d": "Last 7 days", "30d": "Last 30 days" };

export function SloPage() {
  const [period, setPeriod] = useState<SloWindow>("24h");
  const result = useQuery({ queryKey: ["ops", "slo", period], queryFn: ({ signal }) => fetchSlo(period, signal) });
  const columns: DataTableColumn<SloLine>[] = [
    { key: "service", header: "Service", mono: true },
    { key: "requests", header: "Requests", align: "end", render: (l) => Math.round(l.requests).toLocaleString() },
    { key: "availability", header: "Availability", align: "end", render: (l) => percent(l.availability) },
    { key: "availabilityTarget", header: "Target", align: "end", render: (l) => `${(l.availabilityTarget * 100).toFixed(1)} %` },
    { key: "p95", header: "Slowest 5 % above", align: "end", render: (l) => (l.latencyP95Ms === null ? "No traffic" : `${Math.round(l.latencyP95Ms)} ms`) },
    { key: "latencyTarget", header: "Target", align: "end", render: (l) => `${l.latencyTargetMs} ms` },
    {
      key: "budget",
      header: "Error budget left",
      align: "end",
      render: (l) => (l.errorBudgetRemaining === null ? "No traffic" : `${Math.round(Math.max(l.errorBudgetRemaining, -9.99) * 100)} %`),
    },
    {
      key: "state",
      header: "State",
      render: (l) =>
        l.noTraffic ? (
          <Badge tone="neutral">No traffic</Badge>
        ) : l.breached ? (
          <Badge tone="danger">{l.breachedSince ? `Missed since ${when(l.breachedSince)}` : "Missed"}</Badge>
        ) : (
          <Badge tone="success">Met</Badge>
        ),
    },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Service levels"
        description="How each service did against its availability and speed targets. A miss in the hourly check is reported to the operators once, until the service recovers."
      />
      <div className={styles.controls}>
        <FormField id="slo-window" label="Period">
          {(control) => (
            <Select {...control} value={period} onChange={(e) => setPeriod(e.target.value as SloWindow)}>
              {SLO_WINDOWS.map((w) => (
                <option key={w} value={w}>
                  {WINDOW_LABEL[w]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {result.error ? <ProblemMessage problem={result.error} fallback="The service level report could not be loaded." /> : null}
      {result.data ? (
        <StatGroup label="Service level summary">
          <StatTile label="Services measured" value={String(result.data.services.length)} />
          <StatTile label="Missing a target" value={String(result.data.breached)} tone={result.data.breached > 0 ? "danger" : "neutral"} />
          <StatTile label="Measured at" value={when(result.data.generatedAt)} />
        </StatGroup>
      ) : null}
      <Card title="Services">
        <DataTable
          label="Service levels"
          columns={columns}
          rows={result.data?.services ?? []}
          getRowKey={(l) => l.service}
          loading={result.isLoading}
          empty={{ title: "No service reported traffic", description: "Services appear once monitoring has scraped their request metrics." }}
        />
      </Card>
    </div>
  );
}

const STATE_TONE: Record<LicenseItem["status"]["state"], BadgeTone> = { VALID: "success", GRACE: "warning", EXPIRED: "danger" };
const STATE_LABEL: Record<LicenseItem["status"]["state"], string> = { VALID: "Valid", GRACE: "Grace period", EXPIRED: "Expired" };

export function LicensesPage() {
  const result = useQuery({ queryKey: ["ops", "licenses"], queryFn: ({ signal }) => fetchLicenses(signal) });
  const columns: DataTableColumn<LicenseItem>[] = [
    { key: "tenant", header: "Company", render: (l) => l.tenantName || "Provisioned outside the console" },
    { key: "state", header: "State", render: (l) => <Badge tone={STATE_TONE[l.status.state]}>{STATE_LABEL[l.status.state]}</Badge> },
    { key: "expires", header: "Expires", render: (l) => `${l.license.expiresAt} (${l.status.daysToExpiry >= 0 ? `in ${l.status.daysToExpiry} days` : `${-l.status.daysToExpiry} days ago`})` },
    {
      key: "full",
      header: "Full users",
      align: "end",
      render: (l) => (l.usage.countedAt ? `${l.usage.activeFull} of ${l.license.fullUsers}` : `Not counted yet (of ${l.license.fullUsers})`),
    },
    { key: "operational", header: "Operational users", align: "end", render: (l) => `${l.usage.activeOperational} of ${l.license.operationalUsers}` },
    { key: "selfservice", header: "Self-service users", align: "end", render: (l) => `${l.usage.activeSelfservice} of ${l.license.selfserviceUsers}` },
    { key: "counted", header: "Users counted", render: (l) => when(l.usage.countedAt) },
  ];
  const items = result.data ?? [];
  const grace = items.filter((l) => l.status.state === "GRACE").length;
  const expired = items.filter((l) => l.status.state === "EXPIRED").length;
  const expiring = items.filter((l) => l.status.state === "VALID" && l.status.daysToExpiry <= 30).length;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Licences"
        description="Each company's licence, its state today and its active users. The licence check runs every hour; administrators can always sign in and export data."
      />
      {result.error ? <ProblemMessage problem={result.error} fallback="The licences could not be loaded." /> : null}
      {expired > 0 ? <Alert tone="danger">{expired === 1 ? "One licence has expired: new sign-ins are blocked for that company." : `${expired} licences have expired: new sign-ins are blocked for those companies.`}</Alert> : null}
      <StatGroup label="Licence summary">
        <StatTile label="Licences" value={String(items.length)} />
        <StatTile label="Expiring within 30 days" value={String(expiring)} tone={expiring > 0 ? "warning" : "neutral"} />
        <StatTile label="In grace period" value={String(grace)} tone={grace > 0 ? "warning" : "neutral"} />
      </StatGroup>
      <Card title="Licences">
        <DataTable
          label="Licences"
          columns={columns}
          rows={items}
          getRowKey={(l) => l.license.tenantId}
          loading={result.isLoading}
          empty={{ title: "No licences are installed", description: "Install a company's signed licence file to see it here." }}
        />
      </Card>
    </div>
  );
}

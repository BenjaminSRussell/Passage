import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { idempotencyKey, json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findAlert, findTable } from "../../test-utils";
import type { ArchivePartition, DQRule, DQScore, Schedule } from "./api/client";
import { validateRule } from "./DataQualityPage";
import analytics from "./module";
import { validateSchedule } from "./SchedulesPage";

useFakeServer();

const OWNER = "33333333-0000-4000-8000-000000000003";
const render = (path: string) => renderRoutes(analytics.routes, path);
const location = () => screen.getByTestId("location").textContent;

const taxRule: DQRule = {
  ruleId: "vendor_tax_id",
  assetFqn: "core.vendor",
  ruleType: "COMPLETENESS",
  expression: "tax_id",
  thresholdPct: "98",
  severity: "HIGH",
  ownerUserId: OWNER,
  status: "ACTIVE",
  version: 3,
};
const emailRule: DQRule = { ...taxRule, ruleId: "customer_email", assetFqn: "core.customer", ruleType: "VALIDITY", expression: "^[^@]+@[^@]+$", version: 1 };
const score = (ruleId: string, date: string, passPct: string, below: boolean, belowStreak: number): DQScore => ({
  assetFqn: ruleId === "vendor_tax_id" ? "core.vendor" : "core.customer",
  ruleId,
  date,
  passPct,
  failedRows: below ? 30 : 0,
  totalRows: 1000,
  below,
  belowStreak,
});

describe("data-quality scorecard", () => {
  it("shows each rule's latest score, the escalation after three failures and the summary tiles", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/catalog/dq-rules") return json(200, { items: [emailRule, taxRule] });
      if (url.pathname === "/api/v1/catalog/dq-rules/scores")
        return json(200, { items: [score("vendor_tax_id", "2026-10-28", "97.00", true, 3), score("vendor_tax_id", "2026-10-27", "97.10", true, 2), score("customer_email", "2026-10-28", "100.00", false, 0)] });
      return undefined;
    };
    const { container } = render("/analytics/data-quality");
    const table = await findTable({ name: "Data-quality rules" });
    const taxRow = within(table).getByText("vendor_tax_id").closest("tr") as HTMLElement;
    expect(within(taxRow).getByText("97.00%")).toBeInTheDocument();
    expect(within(taxRow).getByText("3 · escalated")).toBeInTheDocument();
    expect(within(table).getByText("100.00%")).toBeInTheDocument();
    const summary = screen.getByRole("list", { name: "Data-quality summary" });
    expect(within(summary).getByText("Below threshold").parentElement).toHaveTextContent("1");
    expect(server.calls.map((c) => c.url.pathname).sort()).toEqual(["/api/v1/catalog/dq-rules", "/api/v1/catalog/dq-rules/scores"]);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(taxRow).getByText("vendor_tax_id"));
    await waitFor(() => expect(location()).toBe("/analytics/data-quality/vendor_tax_id"));
  });

  it("shows the empty state when no rule exists", async () => {
    server.handler = () => json(200, { items: [] });
    render("/analytics/data-quality");
    const table = await findTable({ name: "Data-quality rules" });
    expect(within(table).getByText("No data-quality rules yet")).toBeInTheDocument();
  });

  it("shows the problem and retries when the caller lacks analytics.catalog.steward", async () => {
    let attempt = 0;
    server.handler = (url) => {
      if (++attempt <= 2) return json(403, { code: "AUTH-0003", detail: "missing duty analytics.catalog.steward" });
      return json(200, { items: url.pathname.endsWith("scores") ? [] : [taxRule] });
    };
    render("/analytics/data-quality");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("missing duty analytics.catalog.steward (AUTH-0003)");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    fireEvent.click(within(alert.closest("div") as HTMLElement).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("vendor_tax_id")).toBeInTheDocument();
  });
});

describe("data-quality rule form", () => {
  it("validates like the service before sending anything", () => {
    const errors = validateRule({ ...taxRule, ruleId: "Bad Id", assetFqn: "vendor", thresholdPct: "98.555", ownerUserId: "me", muted: false, ruleType: "VALIDITY", expression: "([" });
    expect(Object.keys(errors).sort()).toEqual(["assetFqn", "expression", "ownerUserId", "ruleId", "thresholdPct"]);
    expect(validateRule({ ...taxRule, muted: false })).toEqual({});
    expect(validateRule({ ...taxRule, thresholdPct: "100.01", muted: false }).thresholdPct).toBeDefined();
  });

  it("creates a rule without If-Match and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(201, { ...taxRule, version: 1 });
      if (url.pathname === "/api/v1/catalog/dq-rules/vendor_tax_id") return json(200, { ...taxRule, version: 1 });
      if (url.pathname === "/api/v1/catalog/dq-rules/scores") return json(200, { items: [] });
      return undefined;
    };
    const { container } = render("/analytics/data-quality/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect(await screen.findByText(/Use 2 to 63 lowercase letters/)).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Rule id"), { target: { value: "vendor_tax_id" } });
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: "core.vendor" } });
    fireEvent.change(screen.getByLabelText("Expression"), { target: { value: "tax_id" } });
    fireEvent.change(screen.getByLabelText("Owner user id"), { target: { value: OWNER } });
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    await waitFor(() => expect(location()).toBe("/analytics/data-quality/vendor_tax_id"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/catalog/dq-rules/vendor_tax_id");
    expect(new Headers(put?.init.headers).get("If-Match")).toBeNull();
    expect(put?.body).toEqual({ assetFqn: "core.vendor", ruleType: "COMPLETENESS", expression: "tax_id", thresholdPct: "98", severity: "MEDIUM", ownerUserId: OWNER, status: "ACTIVE", muted: false });
  });
});

describe("data-quality rule detail", () => {
  const detail = (put: () => ReturnType<typeof json>) => (url: URL, init: RequestInit) => {
    if (init.method === "PUT") return put();
    if (url.pathname === "/api/v1/catalog/dq-rules/vendor_tax_id") return json(200, taxRule);
    if (url.pathname === "/api/v1/catalog/dq-rules/scores") return json(200, { items: [score("vendor_tax_id", "2026-10-28", "97.00", true, 3)] });
    return undefined;
  };

  it("shows the score history and saves with If-Match of the loaded version", async () => {
    server.handler = detail(() => json(200, { ...taxRule, thresholdPct: "95", version: 4 }));
    const { container } = render("/analytics/data-quality/vendor_tax_id");
    const history = await findTable({ name: "Score history" });
    expect(within(history).getByText("2026-10-28")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith("scores"))?.url.searchParams.get("rule")).toBe("vendor_tax_id");
    expect(screen.getByLabelText("Rule id")).toHaveAttribute("readonly");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Threshold %"), { target: { value: "95" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("Rule saved as version 4.")).toHaveAttribute("role", "status");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(new Headers(put?.init.headers).get("If-Match")).toBe('"3"');
    expect((put?.body as { thresholdPct: string }).thresholdPct).toBe("95");
  });

  it("explains a concurrent change (ANA-0113) instead of overwriting it", async () => {
    server.handler = detail(() => json(412, { code: "ANA-0113", detail: "rule vendor_tax_id has changed; reload it and try again" }));
    render("/analytics/data-quality/vendor_tax_id");
    fireEvent.click(await screen.findByRole("button", { name: "Save rule" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this rule");
    expect(alert).toHaveTextContent("(ANA-0113)");
  });

  it("mutes the rule for 7 days through the same versioned save", async () => {
    server.handler = detail(() => json(200, { ...taxRule, muteUntil: "2099-11-04T08:00:00Z", version: 4 }));
    render("/analytics/data-quality/vendor_tax_id");
    fireEvent.click(await screen.findByRole("button", { name: "Mute for 7 days" }));
    expect(await screen.findByText("Muted until 2099-11-04 08:00 UTC")).toBeInTheDocument();
    expect((server.calls.find((c) => c.method === "PUT")?.body as { muted: boolean }).muted).toBe(true);
    expect(screen.getByRole("button", { name: "Unmute rule" })).toBeInTheDocument();
  });

  it("shows not found for an unknown rule", async () => {
    server.handler = (url) => (url.pathname.endsWith("scores") ? json(200, { items: [] }) : json(404, { code: "ANA-0112", detail: "data-quality rule nope not found" }));
    render("/analytics/data-quality/nope");
    expect(await screen.findByRole("heading", { name: "We couldn't find that rule" })).toBeInTheDocument();
  });
});

const PID = "44444444-0000-4000-8000-000000000004";
const partition = (over: Partial<ArchivePartition> = {}): ArchivePartition => ({
  partitionId: PID,
  domain: "ledger",
  tableName: "journal_line",
  period: "2024-01",
  rowsOltp: 120000,
  rowsArchive: 120000,
  contentHashOltp: "ab12cd34ef56ab12cd34ef56",
  contentHashArchive: "ab12cd34ef56ab12cd34ef56",
  status: "DROPPED",
  legalHold: false,
  openPeriod: false,
  movedAt: "2026-03-01T04:10:00Z",
  ...over,
});

describe("archive partitions", () => {
  it("lists partitions, filters by domain and status and flags a failed verification", async () => {
    const mismatch = partition({ partitionId: "55555555-0000-4000-8000-000000000005", period: "2024-02", status: "PENDING", rowsArchive: 119999, contentHashArchive: "ff", movedAt: undefined });
    server.handler = () => json(200, { items: [partition(), mismatch] });
    const { container } = render("/analytics/archive");
    const table = await findTable({ name: "Archive partitions" });
    expect(within(table).getByText("2024-01")).toBeInTheDocument();
    expect(within(table).getByText("Archived")).toBeInTheDocument();
    expect(screen.getByText("1 partitions failed verification")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText(/Domain/), { target: { value: "Ledger!" } });
    fireEvent.click(screen.getByRole("button", { name: "Show partitions" }));
    expect(await screen.findByText("Use a domain name such as ledger or inventory.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(1);

    fireEvent.change(screen.getByLabelText(/Domain/), { target: { value: "ledger" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DROPPED" } });
    fireEvent.click(screen.getByRole("button", { name: "Show partitions" }));
    await waitFor(() => expect(server.calls).toHaveLength(2));
    expect(server.calls[1].url.pathname).toBe("/api/v1/archive/partitions");
    expect(Object.fromEntries(server.calls[1].url.searchParams)).toEqual({ domain: "ledger", status: "DROPPED" });
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = render("/analytics/archive");
    expect(within(await findTable({ name: "Archive partitions" })).getByText("No archive partitions")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty analytics.archive.admin" });
    render("/analytics/archive");
    expect(await findAlert()).toHaveTextContent("missing duty analytics.archive.admin (AUTH-0003)");
  });

  it("restores a dropped partition after confirmation", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(200, partition({ status: "RESTORED", restoreUntil: "2099-11-28T09:00:00Z" }));
      if (url.pathname === `/api/v1/archive/partitions/${PID}`) return json(200, partition());
      return undefined;
    };
    const { container } = render(`/analytics/archive/${PID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Restore for 30 days" }));
    const dialog = screen.getByRole("alertdialog", { name: "Restore this partition?" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore partition" }));
    expect(await screen.findByText("Partition restored until 2099-11-28 09:00 UTC.")).toHaveAttribute("role", "status");
    expect(server.calls.find((c) => c.method === "POST")?.url.pathname).toBe(`/api/v1/archive/partitions/${PID}/restore`);
    expect(screen.queryByRole("button", { name: "Restore for 30 days" })).not.toBeInTheDocument();
  });

  it("offers no restore for a partition still being verified and reports a refused restore", async () => {
    server.handler = () => json(200, partition({ status: "PENDING", movedAt: undefined }));
    const first = render(`/analytics/archive/${PID}`);
    expect(await screen.findByText("Not moved yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Restore/ })).not.toBeInTheDocument();
    first.unmount();

    server.handler = (_url, init) =>
      init.method === "POST" ? json(409, { code: "ANA-0111", detail: "partition is VERIFIED; only a DROPPED partition can be restored" }) : json(200, partition());
    render(`/analytics/archive/${PID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Restore for 30 days" }));
    fireEvent.click(screen.getByRole("button", { name: "Restore partition" }));
    expect(await findAlert()).toHaveTextContent("(ANA-0111)");
  });
});

const SID = "66666666-0000-4000-8000-000000000006";
const schedule: Schedule = {
  scheduleId: SID,
  name: "Weekly revenue",
  targetType: "DASHBOARD",
  targetId: "finance-overview",
  cron: "0 6 * * 1",
  timezone: "Europe/Berlin",
  format: "PDF",
  destination: { type: "EMAIL", targets: ["cfo@example.com"] },
  nextRunAt: "2026-11-02T05:00:00Z",
  failureCount: 0,
  version: 1,
};

describe("report schedules", () => {
  it("lists schedules with their next run in UTC", async () => {
    server.handler = () => json(200, { items: [schedule] });
    const { container } = render("/analytics/schedules");
    const table = await findTable({ name: "Report schedules" });
    expect(within(table).getByText("Weekly revenue")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-02 05:00 UTC")).toBeInTheDocument();
    expect(within(table).getByText("E-mail: cfo@example.com")).toBeInTheDocument();
    expect(server.calls[0].url.pathname).toBe("/api/v1/analytics/schedules");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = render("/analytics/schedules");
    expect(within(await findTable({ name: "Report schedules" })).getByText("No schedules yet")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(503, {});
    render("/analytics/schedules");
    expect(await findAlert()).toHaveTextContent("(HTTP-503)");
  });

  it("validates the destination like the service", () => {
    const base = { name: "x", targetType: "QUERY" as const, targetId: "q1", cron: "0 6 * * 1", timezone: "UTC", format: "XLSX" as const };
    expect(validateSchedule({ ...base, destinationType: "EMAIL", targets: "not-an-address" }).targets).toBe("Enter e-mail addresses.");
    expect(validateSchedule({ ...base, destinationType: "SFTP", targets: "sftp://a.example/x sftp://b.example/y" }).targets).toBe("Enter one sftp://host/path address.");
    expect(validateSchedule({ ...base, destinationType: "INBOX", targets: OWNER })).toEqual({});
    expect(validateSchedule({ ...base, cron: "0 6 * *", destinationType: "INBOX", targets: OWNER }).cron).toBeDefined();
  });

  it("creates a schedule with one Idempotency-Key that a retry reuses", async () => {
    let posts = 0;
    server.handler = (url, init) => {
      if (init.method === "POST") {
        posts += 1;
        return posts === 1 ? json(503, { code: "HTTP-503", detail: "upstream unavailable" }) : json(201, schedule);
      }
      if (url.pathname === `/api/v1/analytics/schedules/${SID}`) return json(200, schedule);
      return undefined;
    };
    const { container } = render("/analytics/schedules/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly revenue" } });
    fireEvent.change(screen.getByLabelText("Report id"), { target: { value: "finance-overview" } });
    fireEvent.change(screen.getByLabelText("Time zone"), { target: { value: "Europe/Berlin" } });
    fireEvent.change(screen.getByLabelText("Recipients"), { target: { value: "cfo@example.com, controller@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Create schedule" }));
    expect(await findAlert()).toHaveTextContent("upstream unavailable");

    fireEvent.click(screen.getByRole("button", { name: "Create schedule" }));
    await waitFor(() => expect(location()).toBe(`/analytics/schedules/${SID}`));
    const sent = server.calls.filter((c) => c.method === "POST");
    expect(sent).toHaveLength(2);
    expect(idempotencyKey(sent[0])).not.toBeNull();
    expect(idempotencyKey(sent[1])).toBe(idempotencyKey(sent[0]));
    expect(sent[0].body).toEqual({
      name: "Weekly revenue",
      targetType: "DASHBOARD",
      targetId: "finance-overview",
      cron: "0 6 * * 1",
      timezone: "Europe/Berlin",
      format: "PDF",
      destination: { type: "EMAIL", targets: ["cfo@example.com", "controller@example.com"] },
    });
    expect(await screen.findByRole("heading", { name: "Weekly revenue" })).toBeInTheDocument();
  });

  it("shows the service's field error for a cron it rejects", async () => {
    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { cron: "value out of range in \"61\"" } });
    render("/analytics/schedules/new");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Bad" } });
    fireEvent.change(screen.getByLabelText("Report id"), { target: { value: "q1" } });
    fireEvent.change(screen.getByLabelText(/^Cron/), { target: { value: "61 6 * * 1" } });
    fireEvent.change(screen.getByLabelText("Recipients"), { target: { value: "a@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Create schedule" }));
    expect(await screen.findByText('value out of range in "61"')).toBeInTheDocument();
  });

  it("shows not found for an unknown schedule", async () => {
    server.handler = () => json(404, { code: "ANA-0112", detail: "schedule x not found" });
    render(`/analytics/schedules/${SID}`);
    expect(await screen.findByRole("heading", { name: "We couldn't find that schedule" })).toBeInTheDocument();
  });
});

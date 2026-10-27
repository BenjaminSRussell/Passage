import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AuditEntry } from "./api/client";
import { defaultAuditValues, formatDetails, toAuditQuery, validateAudit } from "./AuditTrailPage";
import { json, pageViolations, problem, renderAt, server, text, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190f0a0-0000-7000-8000-000000000201";
const PATH = "/api/v1/reporting/audit-trail";

const reopen: AuditEntry = {
  id: "0190f0a0-0000-7000-8000-00000000a001",
  occurredAt: "2026-04-03T09:05:00Z",
  ledgerId: LEDGER,
  period: "2026-03",
  kind: "reopen_approved",
  actorId: "u-cfo",
  objectRef: "period:2026-03",
  details: { approver: "u-cfo", step: 1 },
  traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
  traceUrl: "https://grafana.example/explore?traceId=4bf92f3577b34da6a3ce929d0e0e4736",
};

const posted: AuditEntry = {
  id: "0190f0a0-0000-7000-8000-00000000a002",
  occurredAt: "2026-04-03T09:30:00Z",
  ledgerId: LEDGER,
  period: "2026-03",
  kind: "posted",
  actorId: "u-accountant",
  objectRef: "journal:JV-0042",
  details: { voucher: "JV-0042", amount: "1250.000000" },
};

const run: AuditEntry = {
  id: "0190f0a0-0000-7000-8000-00000000a003",
  occurredAt: "2026-04-03T10:00:00Z",
  kind: "statement_run",
  actorId: "u-controller",
  objectRef: "run:r-1",
  details: { definition: "BS-STD", version: 2 },
  traceId: "req-77",
};

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-04-15T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("audit trail filter rules", () => {
  it("opens on the current month so far and sends to as the day after the end date", () => {
    const v = defaultAuditValues();
    expect(v).toMatchObject({ fromDate: "2026-04-01", toDate: "2026-04-15" });
    expect(validateAudit(v)).toEqual({});
    expect(toAuditQuery(v)).toEqual({ from: "2026-04-01T00:00:00Z", to: "2026-04-16T00:00:00Z" });
  });

  it("accepts a period without dates and trims the other fields", () => {
    const v = { ...defaultAuditValues(), fromDate: "", toDate: "", period: " 2026-03 ", ledger: ` ${LEDGER} `, kind: "posted" as const, actor: " u1 " };
    expect(validateAudit(v)).toEqual({});
    expect(toAuditQuery(v)).toEqual({ period: "2026-03", ledger: LEDGER, kind: "posted", actor: "u1" });
  });

  it("names each field the service would refuse", () => {
    expect(validateAudit({ ...defaultAuditValues(), fromDate: "", toDate: "" })).toEqual({ fromDate: "Enter both dates, or a period." });
    expect(validateAudit({ ...defaultAuditValues(), fromDate: "2025-01-01", toDate: "2026-01-02" }).toDate).toBe("The dates may span at most 366 days.");
    expect(validateAudit({ ...defaultAuditValues(), fromDate: "2026-04-10", toDate: "2026-04-09" }).toDate).toBe("The end date is on or after the start date.");
    expect(Object.keys(validateAudit({ ...defaultAuditValues(), ledger: "GL-1", period: "2026-3" })).sort()).toEqual(["ledger", "period"]);
  });

  it("allows a full year of 366 days", () => {
    expect(validateAudit({ ...defaultAuditValues(), fromDate: "2024-01-01", toDate: "2024-12-31" })).toEqual({});
  });

  it("formats details in key order with nested values as JSON", () => {
    expect(formatDetails({ step: 1, approver: "u-cfo", approvers: ["a", "b"] })).toBe('approver: u-cfo; approvers: ["a","b"]; step: 1');
  });
});

describe("AuditTrailPage", () => {
  it("lists the month's entries with their kind, actor and trace, and opens one", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [reopen, posted, run], limit: 100, nextCursor: null }) : undefined);
    const { container } = renderAt("/finreport/audit-trail");
    const table = await screen.findByRole("table", { name: "Audit entries" });
    expect(await within(table).findByText("Reopen approved")).toBeInTheDocument();
    expect(within(table).getByText("journal:JV-0042")).toBeInTheDocument();
    expect(within(table).getByText("amount: 1250.000000; voucher: JV-0042")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: /Open trace 4bf92f3577b34da6/ })).toHaveAttribute("href", reopen.traceUrl);
    // A trace without a viewer link is shown as text, never as a link.
    expect(within(table).getByText("req-77")).toBeInTheDocument();
    expect(server.calls[0].url.searchParams.get("from")).toBe("2026-04-01T00:00:00Z");
    expect(server.calls[0].url.searchParams.get("to")).toBe("2026-04-16T00:00:00Z");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "2026-04-03T09:05:00Z" }));
    expect(await screen.findByRole("heading", { name: "Reopen approved at 2026-04-03T09:05:00Z" })).toBeInTheDocument();
    expect(screen.getByLabelText("Entry details").textContent).toContain('"approver": "u-cfo"');
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [], limit: 100, nextCursor: null }) : undefined);
    renderAt("/finreport/audit-trail");
    expect(await screen.findByText("No audit entries")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname !== PATH) return undefined;
      return fail ? problem(403, "AUTH-0003", "missing duty reporting.audit.view") : json(200, { items: [posted], limit: 100, nextCursor: null });
    };
    renderAt("/finreport/audit-trail");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the audit trail");
    expect(alert).toHaveTextContent("AUTH-0003");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("journal:JV-0042")).toBeInTheDocument();
  });

  it("applies the filter and loads further pages with the cursor", async () => {
    server.handler = (url) => {
      if (url.pathname !== PATH) return undefined;
      if (url.searchParams.get("cursor") === "c2") return json(200, { items: [run], limit: 100, nextCursor: null });
      return json(200, { items: [reopen, posted], limit: 100, nextCursor: "c2" });
    };
    renderAt("/finreport/audit-trail");
    await screen.findByText("journal:JV-0042");
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-03" } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "reopen_approved" } });
    fireEvent.click(screen.getByRole("button", { name: "Show entries" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("kind") === "reopen_approved")).toBe(true));
    const filtered = server.calls.find((c) => c.url.searchParams.get("kind") === "reopen_approved")!;
    expect(filtered.url.searchParams.get("ledger")).toBe(LEDGER);
    expect(filtered.url.searchParams.get("period")).toBe("2026-03");

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("run:r-1")).toBeInTheDocument();
    const more = server.calls.find((c) => c.url.searchParams.get("cursor") === "c2")!;
    expect(more.url.searchParams.get("kind")).toBe("reopen_approved");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("refuses an unbounded filter without calling the service", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [], limit: 100, nextCursor: null }) : undefined);
    renderAt("/finreport/audit-trail");
    await screen.findByText("No audit entries");
    const before = server.calls.length;
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Show entries" }));
    expect(await screen.findByText("Enter both dates, or a period.")).toBeInTheDocument();
    expect(server.calls.length).toBe(before);
  });

  it("exports the filtered entries and shows the file's SHA-256 and export id", async () => {
    const sha = "a".repeat(64);
    server.handler = (url) => {
      if (url.pathname === PATH) return json(200, { items: [posted], limit: 100, nextCursor: null });
      if (url.pathname === `${PATH}:export`)
        return text(200, "occurred_at,kind\n", { "X-Passage-Export-Id": "e-1", "X-Passage-Export-Sha256": sha, "X-Passage-Export-Entries": "1" });
      return undefined;
    };
    renderAt("/finreport/audit-trail");
    await screen.findByText("journal:JV-0042");
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    const status = await screen.findByRole("status");
    await waitFor(() => expect(status).toHaveTextContent(sha));
    expect(status).toHaveTextContent("1 entry in audit-trail-e-1.csv");
    expect(status).toHaveTextContent("audit-export:e-1");
    const call = server.calls.find((c) => c.url.pathname === `${PATH}:export`)!;
    expect(call.url.searchParams.get("from")).toBe("2026-04-01T00:00:00Z");
  });

  it("explains an export over the size limit", async () => {
    server.handler = (url) => {
      if (url.pathname === PATH) return json(200, { items: [posted], limit: 100, nextCursor: null });
      if (url.pathname === `${PATH}:export`) return problem(422, "REPORT-0030", "The filter matches more than 50000 entries; narrow the period, ledger, kind or dates.");
      return undefined;
    };
    renderAt("/finreport/audit-trail");
    await screen.findByText("journal:JV-0042");
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Too many entries for one export");
    expect(alert).toHaveTextContent("REPORT-0030");
  });
});

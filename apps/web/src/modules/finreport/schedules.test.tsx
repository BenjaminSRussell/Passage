import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Schedule } from "./api/client";
import { blankSchedule, toSchedule, validateSchedule } from "./ScheduleEditorPage";
import { lastFiringBySchedule } from "./SchedulesPage";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190f0a0-0000-7000-8000-000000000201";

const nightly: Schedule = {
  code: "NIGHTLY",
  name: "Nightly pack",
  definition: "BS-STD",
  cron: "0 23 * * *",
  timezone: "UTC",
  ledgerIds: [LEDGER],
  currencyType: "rpt",
  formats: ["pdf", "csv"],
  recipients: ["controller@example.com"],
  attachmentLimitMb: 20,
  enabled: true,
  fiscalYearStartMonth: 1,
  updatedBy: "u1",
  nextRunAt: "2026-04-01T23:00:00Z",
};

const definitions = {
  items: [
    { code: "BS-STD", name: "Balance sheet", kind: "balance_sheet", status: "published", latestVersion: 1, hasDraft: false, updatedAt: "2026-04-01T00:00:00Z" },
    { code: "DRAFT-ONLY", name: "Draft", kind: "custom", status: "draft", latestVersion: 0, hasDraft: true, updatedAt: "2026-04-01T00:00:00Z" },
  ],
};

describe("schedule form rules", () => {
  it("accepts a filled-in schedule and turns line lists into arrays", () => {
    const v = { ...blankSchedule(), code: "NIGHTLY", name: "Nightly", definition: "BS-STD", ledgerIds: `${LEDGER}\n`, recipients: "a@example.com\nb@example.com" };
    expect(validateSchedule(v)).toEqual({});
    expect(toSchedule(v)).toMatchObject({ ledgerIds: [LEDGER], recipients: ["a@example.com", "b@example.com"], attachmentLimitMb: 20, formats: ["pdf", "xlsx"] });
  });

  it("names each field the service would refuse", () => {
    const errors = validateSchedule({
      ...blankSchedule(),
      code: "nightly",
      cron: "0 23 *",
      ledgerIds: "not-a-uuid",
      formats: [],
      recipients: "Ann <ann@example.com>",
      attachmentLimitMb: "30",
    });
    expect(Object.keys(errors).sort()).toEqual(["attachmentLimitMb", "code", "cron", "definition", "formats", "ledgerIds", "name", "recipients"]);
  });

  it("refuses a ledger listed twice", () => {
    expect(validateSchedule({ ...blankSchedule(), ledgerIds: `${LEDGER}\n${LEDGER}` }).ledgerIds).toBe("List each ledger once.");
  });
});

const firing = {
  id: "0190f0a0-0000-7000-8000-000000000f01",
  scheduleCode: "NIGHTLY",
  ledgerId: LEDGER,
  fireTime: "2026-04-01T23:00:00Z",
  periodYear: 2026,
  periodNo: 3,
  status: "completed" as const,
  attempts: 1,
  runId: "0190f0a0-0000-7000-8000-000000000a01",
  startedAt: "2026-04-01T23:00:01Z",
  finishedAt: "2026-04-01T23:00:03Z",
};

describe("schedule firings", () => {
  it("takes the newest firing of each schedule from a newest-first list", () => {
    const older = { ...firing, id: "older", fireTime: "2026-03-01T23:00:00Z" };
    const other = { ...firing, id: "other", scheduleCode: "OTHER" };
    const map = lastFiringBySchedule([firing, other, older]);
    expect(map.get("NIGHTLY")?.id).toBe(firing.id);
    expect(map.get("OTHER")?.id).toBe("other");
  });

  it("shows a failure on one ledger even when another ledger of the same firing completed", () => {
    const failed = { ...firing, id: "failed", ledgerId: "0190f0a0-0000-7000-8000-0000000000b2", status: "failed" as const, errorCode: "REPORT-0004" };
    const older = { ...firing, id: "older-failed", status: "failed" as const, fireTime: "2026-03-01T23:00:00Z" };
    expect(lastFiringBySchedule([firing, failed]).get("NIGHTLY")?.id).toBe("failed");
    expect(lastFiringBySchedule([firing, older]).get("NIGHTLY")?.id).toBe(firing.id);
  });

  it("rejects a month outside 1 to 12", () => {
    expect(validateSchedule({ ...blankSchedule(), fiscalYearStartMonth: "13" }).fiscalYearStartMonth).toBe("Choose a month.");
    expect(toSchedule({ ...blankSchedule(), fiscalYearStartMonth: "4" }).fiscalYearStartMonth).toBe(4);
  });
});

describe("SchedulesPage last run", () => {
  it("shows the last firing of each schedule, done or failed with its problem code", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/schedules") return json(200, { items: [nightly, { ...nightly, code: "BROKEN" }, { ...nightly, code: "NEW" }] });
      if (url.pathname === "/api/v1/reporting/schedule-firings") {
        expect(url.searchParams.get("latest")).toBe("true");
        return json(200, { items: [firing, { ...firing, id: "f2", scheduleCode: "BROKEN", status: "failed", errorCode: "REPORT-0004", attempts: 2 }] });
      }
      return undefined;
    };
    const { container } = renderAt("/finreport/schedules");
    const table = await findTable({ name: "Distribution schedules" });
    expect(await within(table).findByText("Done")).toBeInTheDocument();
    expect(within(table).getByText("for 2026-03", { exact: false })).toBeInTheDocument();
    expect(within(table).getByText("Failed")).toBeInTheDocument();
    expect(within(table).getByText("REPORT-0004")).toBeInTheDocument();
    expect(within(table).getByText("Not yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("still lists the schedules when the firings cannot be loaded", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/schedules") return json(200, { items: [nightly] });
      if (url.pathname === "/api/v1/reporting/schedule-firings") return problem(500, "COM-0500", "down");
      return undefined;
    };
    renderAt("/finreport/schedules");
    expect(await screen.findByRole("button", { name: "NIGHTLY" })).toBeInTheDocument();
    expect(await screen.findByText("Unavailable")).toBeInTheDocument();
  });
});

describe("SchedulesPage", () => {
  it("lists the schedules with their next run and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/schedules") return json(200, { items: [nightly, { ...nightly, code: "PAUSED", cron: "0 6 1 * *", formats: ["xlsx"], enabled: false, nextRunAt: undefined }] });
      if (url.pathname === "/api/v1/reporting/schedules/NIGHTLY") return json(200, nightly, { ETag: '"1"' });
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, definitions);
      return undefined;
    };
    const { container } = renderAt("/finreport/schedules");
    const table = await findTable({ name: "Distribution schedules" });
    expect(await within(table).findByText("0 23 * * * (UTC)")).toBeInTheDocument();
    expect(within(table).getByText("2026-04-01T23:00:00Z")).toBeInTheDocument();
    expect(within(table).getByText("Paused")).toBeInTheDocument();
    expect(within(table).getByText("PDF, CSV")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "NIGHTLY" }));
    expect(await screen.findByRole("heading", { name: "Schedule NIGHTLY" })).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/reporting/schedules" ? json(200, { items: [] }) : undefined);
    renderAt("/finreport/schedules");
    expect(await screen.findByText("No schedules yet")).toBeInTheDocument();
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/reporting/schedules") return undefined;
      return fail ? problem(403, "AUTH-0003", "You need the schedule duty.") : json(200, { items: [nightly] });
    };
    renderAt("/finreport/schedules");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("You need the schedule duty. (AUTH-0003)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "NIGHTLY" })).toBeInTheDocument();
  });
});

describe("ScheduleEditorPage", () => {
  it("creates a schedule over a published definition without If-Match", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, definitions);
      if (url.pathname === "/api/v1/reporting/schedules/NIGHTLY" && init.method === "PUT") {
        return json(200, { ...JSON.parse(String(init.body)), updatedBy: "u1", nextRunAt: "2026-04-01T23:00:00Z" }, { ETag: '"1"' });
      }
      if (url.pathname === "/api/v1/reporting/schedules/NIGHTLY") return json(200, nightly, { ETag: '"1"' });
      return undefined;
    };
    const { container } = renderAt("/finreport/schedules/new");
    await screen.findByRole("option", { name: "BS-STD: Balance sheet" });
    const select = screen.getByLabelText("Definition");
    expect(within(select).queryByRole("option", { name: "DRAFT-ONLY: Draft" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Schedule code"), { target: { value: "nightly" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Nightly pack" } });
    fireEvent.change(select, { target: { value: "BS-STD" } });
    fireEvent.change(screen.getByLabelText("Ledgers"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Recipients"), { target: { value: "controller@example.com" } });
    fireEvent.click(screen.getByLabelText("CSV"));
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));

    // The first save moves to the schedule's own route, which reloads it (the notice is hidden while it
    // loads). Wait for that reload before looking for the notice, or findByText can return the notice
    // from the render before the reload and it is detached by the time it is asserted.
    await waitFor(() =>
      expect(server.calls.some((c) => c.method === "GET" && c.url.pathname === "/api/v1/reporting/schedules/NIGHTLY")).toBe(true),
    );
    expect(await screen.findByText(/Schedule saved\. Next run: 2026-04-01T23:00:00Z\./)).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/reporting/schedules/NIGHTLY");
    expect(put && header(put, "If-Match")).toBeNull();
    expect(put?.body).toEqual({
      code: "NIGHTLY",
      name: "Nightly pack",
      definition: "BS-STD",
      cron: "0 23 * * *",
      timezone: "UTC",
      ledgerIds: [LEDGER],
      currencyType: "acct",
      formats: ["pdf", "xlsx", "csv"],
      recipients: ["controller@example.com"],
      attachmentLimitMb: 20,
      enabled: true,
      fiscalYearStartMonth: 1,
    });
  });

  it("does not send a schedule with field errors", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/reporting/definitions" ? json(200, definitions) : undefined);
    renderAt("/finreport/schedules/new");
    await screen.findByRole("option", { name: "BS-STD: Balance sheet" });
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("Choose a published definition.")).toBeInTheDocument();
    expect(screen.getByLabelText("Schedule code")).toHaveAttribute("aria-invalid", "true");
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("replaces a schedule with the ETag it read and offers a reload on a conflict", async () => {
    let revision = 4;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, definitions);
      if (url.pathname !== "/api/v1/reporting/schedules/NIGHTLY") return undefined;
      if (init.method === "PUT") return problem(412, "conflict_version", "schedule revision is 5, not 4");
      return json(200, nightly, { ETag: `"${revision}"` });
    };
    renderAt("/finreport/schedules/NIGHTLY");
    const name = await screen.findByLabelText("Name");
    expect(screen.getByLabelText("Schedule code")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Currency type")).toHaveValue("rpt");
    fireEvent.change(name, { target: { value: "Nightly statement pack" } });
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this schedule");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put && header(put, "If-Match")).toBe('"4"');
    expect(put?.body).not.toHaveProperty("nextRunAt");
    revision = 5;
    fireEvent.click(within(alert).getByRole("button", { name: "Reload the schedule" }));
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Nightly pack"));
  });

  it("explains an unpublished definition", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") return problem(500, "COM-0500", "down");
      if (url.pathname === "/api/v1/reporting/schedules/NIGHTLY" && init.method === "PUT") return problem(422, "REPORT-0001", "Definition OLD-DEF has no published version.");
      if (url.pathname === "/api/v1/reporting/schedules/NIGHTLY") return json(200, { ...nightly, definition: "OLD-DEF" }, { ETag: '"2"' });
      return undefined;
    };
    renderAt("/finreport/schedules/NIGHTLY");
    await waitFor(() => expect(screen.getByLabelText("Definition").tagName).toBe("INPUT"));
    expect(screen.getByLabelText("Definition")).toHaveValue("OLD-DEF");
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await findAlert()).toHaveTextContent("That definition has no published version");
  });

  it("reports a schedule that does not exist", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, definitions);
      if (url.pathname === "/api/v1/reporting/schedules/GONE") return problem(404, "REPORT-0019", "schedule GONE does not exist");
      return undefined;
    };
    renderAt("/finreport/schedules/GONE");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("This schedule does not exist");
    expect(within(alert).queryByRole("button")).toBeNull();
  });
});

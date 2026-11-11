import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findTable } from "../../test-utils";
import type { ArchivePartition, DQRule } from "./api/client";
import analytics from "./module";

// Inquiry filters from PRD §9.46.14: the data-quality scorecard by asset, rule
// type and owner; the archive partition inquiry by domain, period and status.

useFakeServer();

const OWNER = "33333333-0000-4000-8000-000000000003";
const render = (path: string) => renderRoutes(analytics.routes, path);

const rule: DQRule = {
  ruleId: "vendor_iban_unique",
  assetFqn: "core.vendor",
  ruleType: "UNIQUENESS",
  expression: "iban",
  thresholdPct: "98",
  severity: "HIGH",
  ownerUserId: OWNER,
  status: "ACTIVE",
  version: 1,
};

const partition: ArchivePartition = {
  partitionId: "44444444-0000-4000-8000-000000000004",
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
};

const rulesCalls = () => server.calls.filter((c) => c.url.pathname === "/api/v1/catalog/dq-rules");

describe("data-quality scorecard filters", () => {
  it("sends asset, rule type and owner to the service and narrows the scores to the asset", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/catalog/dq-rules") return json(200, { items: url.searchParams.has("ruleType") ? [rule] : [] });
      if (url.pathname === "/api/v1/catalog/dq-rules/scores") return json(200, { items: [] });
      return undefined;
    };
    const { container } = render("/analytics/data-quality");
    await findTable({ name: "Data-quality rules" });
    expect(Object.fromEntries(rulesCalls()[0].url.searchParams)).toEqual({});
    expect(pageViolations(container)).toEqual([]);

    const filter = screen.getByRole("form", { name: "Scorecard filter" });
    fireEvent.change(within(filter).getByLabelText(/Asset/), { target: { value: "core.vendor" } });
    fireEvent.change(within(filter).getByLabelText("Rule type"), { target: { value: "UNIQUENESS" } });
    fireEvent.change(within(filter).getByLabelText(/^Owner/), { target: { value: OWNER } });
    fireEvent.click(within(filter).getByRole("button", { name: "Show rules" }));

    await waitFor(() => expect(rulesCalls()).toHaveLength(2));
    expect(Object.fromEntries(rulesCalls()[1].url.searchParams)).toEqual({ asset: "core.vendor", ruleType: "UNIQUENESS", owner: OWNER });
    const scores = server.calls.filter((c) => c.url.pathname.endsWith("/scores"));
    expect(scores[scores.length - 1].url.searchParams.get("asset")).toBe("core.vendor");
    const table = await findTable({ name: "Data-quality rules" });
    expect(await within(table).findByText("vendor_iban_unique")).toBeInTheDocument();
  });

  it("refuses a malformed asset, takes no typed text as an owner, and does not call the service", async () => {
    server.handler = () => json(200, { items: [] });
    render("/analytics/data-quality");
    await findTable({ name: "Data-quality rules" });
    const before = server.calls.length;
    const filter = screen.getByRole("form", { name: "Scorecard filter" });
    fireEvent.change(within(filter).getByLabelText(/Asset/), { target: { value: "Vendor" } });
    fireEvent.change(within(filter).getByLabelText(/^Owner/), { target: { value: "alice" } });
    expect(within(filter).getByLabelText(/^Owner/)).toHaveAttribute("data-value", "");
    fireEvent.click(within(filter).getByRole("button", { name: "Show rules" }));
    expect(await screen.findByText("Use a dotted lower-case asset such as core.vendor.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(before);
  });

  it("says no rule matches when a filter is set and nothing comes back", async () => {
    server.handler = () => json(200, { items: [] });
    render("/analytics/data-quality");
    await findTable({ name: "Data-quality rules" });
    const filter = screen.getByRole("form", { name: "Scorecard filter" });
    fireEvent.change(within(filter).getByLabelText("Rule type"), { target: { value: "FRESHNESS" } });
    fireEvent.click(within(filter).getByRole("button", { name: "Show rules" }));
    const table = await findTable({ name: "Data-quality rules" });
    expect(await within(table).findByText("No rules match the filter")).toBeInTheDocument();
  });
});

describe("archive partition period filter", () => {
  it("filters by period and refuses a period that is not a month", async () => {
    server.handler = () => json(200, { items: [partition] });
    render("/analytics/archive");
    await findTable({ name: "Archive partitions" });
    expect(server.calls).toHaveLength(1);

    fireEvent.change(screen.getByLabelText(/Period/), { target: { value: "2024-13" } });
    fireEvent.click(screen.getByRole("button", { name: "Show partitions" }));
    expect(await screen.findByText("Use a month as YYYY-MM, for example 2024-01.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(1);

    fireEvent.change(screen.getByLabelText(/Period/), { target: { value: "2024-01" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DROPPED" } });
    fireEvent.click(screen.getByRole("button", { name: "Show partitions" }));
    await waitFor(() => expect(server.calls).toHaveLength(2));
    expect(Object.fromEntries(server.calls[1].url.searchParams)).toEqual({ period: "2024-01", status: "DROPPED" });
    expect(screen.queryByText("Use a month as YYYY-MM, for example 2024-01.")).not.toBeInTheDocument();
  });
});

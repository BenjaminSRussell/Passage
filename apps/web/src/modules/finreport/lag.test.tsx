import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { toRunRequest, type RunValues } from "./NewRunPage";
import { json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190f0a0-0000-7000-8000-000000000201";

const values: RunValues = {
  definition: "PL-STD",
  version: "",
  ledgerId: LEDGER,
  currencyType: "acct",
  year: "2026",
  period: "3",
  periodsPerYear: "12",
  firstYear: "2025",
  includeAdjustmentPeriods: false,
  hierarchy: "",
  hierarchyVersion: "",
  normalBalance: "debit",
  accountFrom: "",
  accountTo: "",
};

const definitions = {
  items: [{ code: "PL-STD", name: "Income statement", kind: "income_statement", status: "published", latestVersion: 2, hasDraft: false, updatedAt: "" }],
};

describe("lag gate on the run form", () => {
  it("sends allowStale only when the box is ticked", () => {
    expect(toRunRequest(values).allowStale).toBeUndefined();
    expect(toRunRequest({ ...values, allowStale: true }).allowStale).toBe(true);
  });

  it("offers Run anyway after REPORT-0004 and repeats the run with allowStale", async () => {
    let posts = 0;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, definitions);
      if (url.pathname === "/api/v1/reporting/runs" && init.method === "POST") {
        posts += 1;
        return posts === 1 ? problem(409, "REPORT-0004", "Read model is 90 s behind ledger L (limit 30 s).") : problem(422, "REPORT-0001", "stop here");
      }
      return undefined;
    };
    const { container } = renderAt("/finreport/runs/new");
    const select = await screen.findByLabelText("Definition");
    await waitFor(() => expect(select.querySelectorAll("option")).toHaveLength(2));
    fireEvent.change(select, { target: { value: "PL-STD" } });
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByText("The read model is behind the ledger")).toBeInTheDocument();
    expect(screen.getByText(/90 s behind/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Run anyway" }));
    await waitFor(() => expect(posts).toBe(2));
    const sent = server.calls.filter((c) => c.method === "POST");
    expect(sent[0].body).not.toHaveProperty("allowStale");
    expect(sent[1].body).toMatchObject({ allowStale: true });
    expect(screen.getByLabelText("Accept stale results")).toBeChecked();
  });
});

describe("lag panel", () => {
  it("flags a ledger behind the gate and leaves a caught up one alone", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/runs") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/lag") {
        return json(200, {
          maxLagSeconds: 30,
          items: [
            { ledgerId: LEDGER, postingSequence: 41, updatedAt: "2026-04-02T07:59:00Z", entriesApplied: 41, entriesPending: 2, lagSeconds: 95, hwmLedger: 44 },
            { ledgerId: "0190f0a0-0000-7000-8000-000000000202", postingSequence: 9, updatedAt: "2026-04-02T07:59:00Z", entriesApplied: 9, entriesPending: 0, lagSeconds: 0, hwmLedger: 9 },
          ],
        });
      }
      return undefined;
    };
    const { container } = renderAt("/finreport");
    expect(await screen.findByText("Statements are stale for this ledger")).toBeInTheDocument();
    expect(screen.getAllByText("Statements are stale for this ledger")).toHaveLength(1);
    expect(screen.getByText(/at least 95 s behind the ledger \(limit 30 s\)/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

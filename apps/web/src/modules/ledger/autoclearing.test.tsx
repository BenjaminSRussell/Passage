import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AutoClearingRule, AutoClearingRun, ToleranceGroup } from "./api/client";
import { emptyAutoRule, parseCriteria, validateAutoRule } from "./AutoClearingRuleFormPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const RULE = "0190a000-0000-7000-8000-0000000000b1";
const RUN = "0190a000-0000-7000-8000-0000000000c1";
const DOC = "0190a000-0000-7000-8000-0000000000f1";
const api = "/api/v1/ledger";

function rule(patch: Partial<AutoClearingRule> = {}): AutoClearingRule {
  return {
    id: RULE,
    ledgerId: LEDGER,
    name: "GR/IR matching",
    accountFrom: "2100",
    accountTo: "2199",
    criteria: ["assignment", "dim.DEPT"],
    allowTolerance: true,
    toleranceGroup: "STD",
    sequence: 10,
    active: true,
    version: 3,
    createdBy: "boss",
    createdAt: "2026-03-01T00:00:00Z",
    updatedAt: "2026-03-01T00:00:00Z",
    ...patch,
  };
}

const tolerance: ToleranceGroup = {
  ledgerId: LEDGER,
  code: "STD",
  name: "Standard",
  debitLimit: "0.05",
  creditLimit: "0.05",
  percentLimit: "0",
  differenceAccount: "6990",
  active: true,
  version: 1,
  createdAt: "2026-03-01T00:00:00Z",
  updatedAt: "2026-03-01T00:00:00Z",
};

function run(patch: Partial<AutoClearingRun> = {}): AutoClearingRun {
  return {
    ledgerId: LEDGER,
    mode: "simulate",
    clearingDate: "2026-03-31",
    groupsFound: 3,
    groupsCleared: 2,
    groupsSkipped: 1,
    itemsCleared: 4,
    unmatchedItems: 5,
    truncated: false,
    groups: [
      { no: 1, ruleId: RULE, account: "2100", itemCount: 2, net: "0.00", status: "proposed" },
      { no: 2, ruleId: RULE, account: "2100", itemCount: 2, net: "0.02", status: "proposed" },
      { no: 3, ruleId: RULE, account: "2100", itemCount: 2, net: "1.00", status: "skipped", reasonCode: "LEDGER_CLEARING_UNBALANCED", reason: "the group nets to 1" },
    ],
    ...patch,
  };
}

const stored = (patch: Partial<AutoClearingRun> = {}) =>
  run({
    id: RUN,
    mode: "clear",
    createdBy: "batch",
    createdAt: "2026-03-31T02:00:00Z",
    ruleId: RULE,
    groups: [
      { no: 1, ruleId: RULE, account: "2100", itemCount: 2, net: "0.00", status: "cleared", clearingDocumentId: DOC },
      { no: 2, ruleId: RULE, account: "2100", itemCount: 2, net: "1.00", status: "skipped", reasonCode: "LEDGER_CLEARING_UNBALANCED", reason: "the group nets to 1" },
    ],
    ...patch,
  });

describe("rule form helpers", () => {
  it("splits criteria by line or comma", () => {
    expect(parseCriteria("assignment\n dim.DEPT ,amount_txn\n\n")).toEqual(["assignment", "dim.DEPT", "amount_txn"]);
  });

  it("mirrors the rule validation", () => {
    expect(Object.keys(validateAutoRule({ ...emptyAutoRule })).sort()).toEqual(["accountFrom", "accountTo", "name"]);
    const ok = { ...emptyAutoRule, name: "R", accountFrom: "2100", accountTo: "2199" };
    expect(validateAutoRule(ok)).toEqual({});
    expect(validateAutoRule({ ...ok, accountFrom: "2200" }).accountTo).toBeDefined();
    expect(validateAutoRule({ ...ok, criteria: "colour" }).criteria).toBeDefined();
    expect(validateAutoRule({ ...ok, criteria: "assignment\nassignment" }).criteria).toBeDefined();
    expect(validateAutoRule({ ...ok, criteria: "a\nb\nc\nd\ne\nf" }).criteria).toBeDefined();
    expect(validateAutoRule({ ...ok, criteria: "" }).criteria).toBeDefined();
    expect(validateAutoRule({ ...ok, allowTolerance: true }).toleranceGroup).toBeDefined();
    expect(validateAutoRule({ ...ok, allowTolerance: true, toleranceGroup: "STD" })).toEqual({});
    expect(validateAutoRule({ ...ok, sequence: "0" }).sequence).toBeDefined();
  });
});

describe("automatic clearing rules", () => {
  const list = (items: AutoClearingRule[]) => (url: URL) => (url.pathname.endsWith("/auto-clearing-rules") ? json(200, { items }) : undefined);

  it("asks for a ledger first", () => {
    renderAt("/ledger/auto-clearing-rules");
    expect(screen.getByText("Enter a ledger id to see its automatic clearing rules.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("lists the rules in order", async () => {
    server.handler = list([rule(), rule({ id: "x", name: "Old", active: false, allowTolerance: false, toleranceGroup: undefined, sequence: 20 })]);
    const { container } = renderAt(`/ledger/auto-clearing-rules?ledger=${LEDGER}`);
    const table = await findTable({ name: "Automatic clearing rules" });
    expect(within(table).getByText("GR/IR matching").closest("tr")?.textContent).toContain("2100 to 2199");
    expect(within(table).getByText("GR/IR matching").closest("tr")?.textContent).toContain("assignment, dim.DEPT");
    expect(within(table).getByText("Old").closest("tr")?.textContent).toContain("Inactive");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    server.handler = list([]);
    renderAt(`/ledger/auto-clearing-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("No automatic clearing rules yet")).toBeInTheDocument();
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) => (url.pathname.endsWith("/auto-clearing-rules") ? (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })) : undefined);
    renderAt(`/ledger/auto-clearing-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No automatic clearing rules yet")).toBeInTheDocument();
  });

  it("checks the form, then creates with PUT under a new id", async () => {
    server.handler = (url, init) => {
      if (url.pathname.includes("/auto-clearing-rules/") && init.method === "PUT") return json(201, rule());
      if (url.pathname.endsWith("/tolerance-groups")) return json(200, { items: [tolerance] });
      if (url.pathname.endsWith("/auto-clearing-rules")) return json(200, { items: [rule()] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/auto-clearing-rules/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("Enter a name of up to 80 characters.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "GR/IR matching" } });
    fireEvent.change(screen.getByLabelText("First account"), { target: { value: "2100" } });
    fireEvent.change(screen.getByLabelText("Last account"), { target: { value: "2199" } });
    fireEvent.change(screen.getByLabelText("Criteria"), { target: { value: "assignment\ndim.DEPT" } });
    fireEvent.click(screen.getByLabelText("Allow a tolerance group to write off a small difference"));
    await screen.findByRole("option", { name: "STD - Standard" });
    fireEvent.change(screen.getByLabelText("Tolerance group"), { target: { value: "STD" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/auto-clearing-rules"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toMatch(new RegExp(`${api}/ledgers/${LEDGER}/auto-clearing-rules/[0-9a-f-]{36}$`));
    expect(put?.body).toEqual({ name: "GR/IR matching", accountFrom: "2100", accountTo: "2199", criteria: ["assignment", "dim.DEPT"], allowTolerance: true, toleranceGroup: "STD", sequence: 100, active: true });
  });

  it("edits a rule at its version and shows a save failure", async () => {
    let conflict = true;
    server.handler = (url, init) => {
      if (url.pathname === `${api}/ledgers/${LEDGER}/auto-clearing-rules/${RULE}`) {
        if (init.method === "PUT") return conflict ? json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the rule changed" }) : json(200, rule({ version: 4 }));
        return json(200, rule());
      }
      if (url.pathname.endsWith("/tolerance-groups")) return json(200, { items: [tolerance] });
      if (url.pathname.endsWith("/auto-clearing-rules")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/ledger/auto-clearing-rules/${RULE}?ledger=${LEDGER}`);
    expect(await screen.findByDisplayValue("GR/IR matching")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/LEDGER_VERSION_CONFLICT/)).toBeInTheDocument();
    conflict = false;
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/auto-clearing-rules"));
    const puts = server.calls.filter((c) => c.method === "PUT");
    expect(new Headers(puts[0].init.headers).get("If-Match")).toBe('"3"');
  });
});

describe("automatic clearing run", () => {
  it("asks for a ledger first", () => {
    renderAt("/ledger/auto-clearing");
    expect(screen.getByText("Enter a ledger id to run automatic clearing on it.")).toBeInTheDocument();
  });

  it("simulates, then clears with one key per attempt and opens the stored run", async () => {
    let failFirst = true;
    server.handler = (url, init) => {
      if (url.pathname === `${api}/auto-clearing:run`) {
        const body = JSON.parse(String(init.body));
        if (body.mode === "simulate") return json(200, run());
        if (failFirst) {
          failFirst = false;
          return json(500, { code: "COM-0500", detail: "internal error" });
        }
        return json(201, stored());
      }
      if (url.pathname === `${api}/auto-clearing-runs`) return json(200, { items: [stored()] });
      if (url.pathname === `${api}/auto-clearing-runs/${RUN}`) return json(200, stored());
      if (url.pathname.endsWith("/auto-clearing-rules")) return json(200, { items: [rule(), rule({ id: "off", name: "Off", active: false })] });
      if (url.pathname === `${api}/ledgers/${LEDGER}/auto-clearing-rules/${RULE}`) return json(200, rule());
      return undefined;
    };
    const { container } = renderAt(`/ledger/auto-clearing?ledger=${LEDGER}`);
    const earlier = await findTable({ name: "Earlier runs" });
    expect(within(earlier).getByText("batch")).toBeInTheDocument();
    await screen.findByRole("option", { name: "GR/IR matching" });
    expect(screen.queryByRole("option", { name: "Off" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Clear \d/ })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Clearing date"), { target: { value: "31/03/2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText("Dates use the form YYYY-MM-DD.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Clearing date"), { target: { value: "2026-03-31" } });
    fireEvent.change(screen.getByLabelText("Rule"), { target: { value: RULE } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    const groups = await findTable({ name: "Groups" });
    expect(within(groups).getAllByText("Would clear")).toHaveLength(2);
    expect(within(groups).getByText(/the group nets to 1 \(LEDGER_CLEARING_UNBALANCED\)/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Run summary" }).textContent).toContain("Groups found");
    expect(pageViolations(container)).toEqual([]);
    const sim = server.calls.find((c) => c.url.pathname.endsWith(":run"));
    expect(sim?.body).toEqual({ ledgerId: LEDGER, ruleId: RULE, clearingDate: "2026-03-31", mode: "simulate" });
    expect(idempotencyKey(sim as never)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Clear 2 groups" }));
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear 2 groups" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/auto-clearing-runs/${RUN}`));
    const posts = server.calls.filter((c) => c.url.pathname.endsWith(":run") && (c.body as { mode: string }).mode === "clear");
    expect(posts).toHaveLength(2);
    expect(idempotencyKey(posts[0])).toBeTruthy();
    expect(idempotencyKey(posts[1])).toBe(idempotencyKey(posts[0]));
  });

  it("shows the empty runs state and a simulation with no groups", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${api}/auto-clearing:run`) return json(200, run({ groupsFound: 0, groupsCleared: 0, groupsSkipped: 0, itemsCleared: 0, groups: [] }));
      if (url.pathname === `${api}/auto-clearing-runs`) return json(200, { items: [] });
      if (url.pathname.endsWith("/auto-clearing-rules")) return json(200, { items: [] });
      void init;
      return undefined;
    };
    renderAt(`/ledger/auto-clearing?ledger=${LEDGER}`);
    expect(await screen.findByText("No runs yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText("No groups found")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Clear \d/ })).not.toBeInTheDocument();
  });

  it("shows a refused run and a failed history load", async () => {
    server.handler = (url) => {
      if (url.pathname === `${api}/auto-clearing:run`) return json(404, { code: "LEDGER_AUTO_CLEARING_RULE_NOT_FOUND", detail: "automatic clearing rule not found" });
      if (url.pathname === `${api}/auto-clearing-runs`) return json(500, { code: "COM-0500", detail: "internal error" });
      if (url.pathname.endsWith("/auto-clearing-rules")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/ledger/auto-clearing?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the earlier runs")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText(/LEDGER_AUTO_CLEARING_RULE_NOT_FOUND/)).toBeInTheDocument();
  });

  it("shows a stored run with its group log, and a 404", async () => {
    server.handler = (url) => {
      if (url.pathname === `${api}/auto-clearing-runs/${RUN}`) return json(200, stored({ truncated: true }));
      if (url.pathname === `${api}/ledgers/${LEDGER}/auto-clearing-rules/${RULE}`) return json(200, rule());
      return undefined;
    };
    const { container } = renderAt(`/ledger/auto-clearing-runs/${RUN}`);
    const groups = await findTable({ name: "Groups" });
    expect(within(groups).getByText("Clearing document").getAttribute("href")).toBe(`/ledger/clearings/${DOC}?ledger=${LEDGER}`);
    expect(await screen.findByText(/Rule: GR\/IR matching/)).toBeInTheDocument();
    expect(screen.getByText("Not every open item was read")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a missing run as an error", async () => {
    server.handler = () => json(404, { code: "LEDGER_AUTO_CLEARING_RUN_NOT_FOUND", detail: "automatic clearing run not found" });
    renderAt(`/ledger/auto-clearing-runs/${RUN}`);
    expect(await screen.findByText("Could not load the run")).toBeInTheDocument();
  });
});

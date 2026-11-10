import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { ChecklistItem, CutoverPlan, GoApproval, GoNoGoDecision, Readiness } from "./api/client";
import { validateDecision, type DecisionValues } from "./CutoverDecisionPage";
import migrationFactory from "./module";

const PLAN = "0195f0a0-0000-7000-8000-0000000000c1";
const RUN = "0195f0a0-0000-7000-8000-0000000000a1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const attestedCodes = ["opening_tb_signed", "load_test"];
const items = (system: "MET" | "OPEN"): ChecklistItem[] => [
  { code: "packs_reconciled", title: "All packs of the plan RECONCILED for every entity, 0 MISMATCH", source: "system", status: system, detail: system === "MET" ? "2 pack/entity runs RECONCILED." : "no run for gl_balances/USMF.", references: system === "MET" ? [RUN] : [] },
  { code: "parallel_run_signed_off", title: "Parallel run for the prior month SIGNED_OFF with 0 unexplained", source: "system", status: system, detail: "", references: [] },
  ...attestedCodes.map((code): ChecklistItem => ({ code, title: `Attested ${code}`, source: "attested", status: "UNCONFIRMED", detail: "Not confirmed with a reference to its evidence.", references: [] })),
];

const readiness = (over: Partial<Readiness> = {}): Readiness => ({
  planId: PLAN,
  version: 2,
  rolledBack: false,
  systemReady: false,
  items: items("OPEN"),
  openItems: items("OPEN").map((i) => i.code),
  decisions: [],
  stagedPacks: [],
  ...over,
});

const noGo: GoNoGoDecision = {
  seq: 1,
  decision: "NO_GO",
  approvers: ["cfo"],
  reason: "UAT not signed",
  decidedBy: "manager",
  decidedAt: "2027-01-01T20:00:00Z",
  planVersion: 2,
  checklist: items("OPEN"),
  waivers: [],
};

const planAfter: CutoverPlan = {
  planId: PLAN, t0: "2027-01-01T00:00:00Z", packs: ["open_items"], entities: ["USMF"], abortOffsetMin: 2160, rolledBack: false, approvers: [], version: 3,
  createdAt: "2026-12-01T00:00:00Z", stepsDone: 0, stepsTotal: 0, openGoNoGo: [], abortDue: false,
};

const goPath = `/api/v1/cutover/plans/${PLAN}/go-no-go`;

const pendingGo: GoApproval = {
  round: 1,
  approver: "alice",
  attestations: [{ code: "load_test", confirmed: true, reference: "https://e/load" }],
  waivers: [],
  planVersion: 2,
  approvedAt: "2027-01-01T21:00:00Z",
};

describe("decision form helpers", () => {
  const base: DecisionValues = { decision: "GO", reason: "", attestations: {}, waivers: {} };
  it("applies the service's rules before sending and never sends approvers", () => {
    expect(validateDecision({ ...base, decision: "NO_GO" }, 2).errors.reason).toBeDefined();
    expect(validateDecision({ ...base, attestations: { load_test: { confirmed: true, reference: " " } } }, 2).errors.attestations).toMatch(/CUT-1606/);
    const ok = validateDecision({ ...base, attestations: { load_test: { confirmed: true, reference: " https://e/1 " }, dr_game_day: { confirmed: false, reference: "x" } } }, 4);
    expect(ok.input).toEqual({ decision: "GO", attestations: [{ code: "load_test", confirmed: true, reference: "https://e/1" }], version: 4 });
  });

  it("sends no attestations when approving a pending GO", () => {
    const second = validateDecision({ ...base, attestations: { load_test: { confirmed: true, reference: " " } } }, 3, pendingGo);
    expect(second).toEqual({ errors: {}, input: { decision: "GO", version: 3 } });
    expect(validateDecision({ ...base, decision: "NO_GO", reason: "late" }, 3, pendingGo).input).toEqual({ decision: "NO_GO", reason: "late", attestations: [], version: 3 });
  });
});

describe("Go/no-go decision page", () => {
  it("shows the checklist with its evidence and the decisions so far", async () => {
    handler = (url) => (url.pathname === goPath ? json(200, readiness({ systemReady: true, items: [...items("MET")], decisions: [noGo] })) : undefined);
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(screen.getByRole("heading", { name: "Go/no-go decision" })).toBeInTheDocument();
    const checklist = await findTable({ name: "Go/no-go checklist" });
    const rows = within(checklist).getAllByRole("row");
    expect(rows).toHaveLength(5);
    expect(rows[1]).toHaveTextContent("Migration factory");
    expect(rows[1]).toHaveTextContent("Met");
    expect(within(rows[1]).getByRole("link", { name: RUN.slice(0, 8) })).toHaveAttribute("href", `/migration-factory/runs/${RUN}`);
    expect(rows[3]).toHaveTextContent("Unconfirmed");
    const decisions = await findTable({ name: "Decisions" });
    expect(within(decisions).getAllByRole("row")[1]).toHaveTextContent("UAT not signed");
    expect(screen.getByText("2 / 4")).toBeInTheDocument();
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("shows loading, then an error with a retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "database down" });
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(screen.getByText("Loading the checklist.")).toBeInTheDocument();
    expect(await screen.findByText(/database down/)).toBeInTheDocument();
    handler = () => json(200, readiness());
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "Record decision" })).toBeInTheDocument();
  });

  it("shows an empty decision table", async () => {
    handler = () => json(200, readiness());
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(await screen.findByText("No decision recorded yet")).toBeInTheDocument();
  });

  it("records a no-go with its reason, then reloads", async () => {
    let decided = false;
    handler = (url, init) => {
      if (url.pathname !== goPath) return undefined;
      if (init.method === "POST") {
        decided = true;
        return json(201, { decision: noGo, plan: planAfter });
      }
      return json(200, readiness({ decisions: decided ? [noGo] : [] }));
    };
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    await screen.findByRole("button", { name: "Record decision" });
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText("A NO_GO decision needs a reason.")).toBeInTheDocument();
    expect(screen.getByText("You are recorded as the approver of this no-go.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Approvers")).not.toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    await userEvent.type(screen.getByLabelText("Reason"), "UAT not signed");
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(1));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ decision: "NO_GO", reason: "UAT not signed", attestations: [], version: 2 });
    expect(await screen.findByText("NO_GO recorded with cfo as approvers.")).toBeInTheDocument();
    const decisions = await findTable({ name: "Decisions" });
    await waitFor(() => expect(within(decisions).getAllByRole("row")[1]).toHaveTextContent("UAT not signed"));
  });

  it("waives a pack that is staged and not posted, with a reason, and sends it with the GO", async () => {
    handler = (url, init) => {
      if (url.pathname !== goPath) return undefined;
      if (init.method === "POST") return json(202, { pendingGo: { ...pendingGo, waivers: [{ pack: "onhand", reason: "posted manually" }] }, plan: planAfter });
      return json(200, readiness({ stagedPacks: [{ pack: "onhand", reason: "staged in the factory only, not posted to the inventory ledger" }] }));
    };
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    await screen.findByRole("button", { name: "Record decision" });
    await userEvent.selectOptions(screen.getByLabelText("Decision"), "GO");
    await userEvent.click(screen.getByRole("checkbox", { name: /onhand: staged in the factory only/ }));
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText(/Every waived pack needs a reason/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reason for waiving onhand"), { target: { value: " posted manually " } });
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Approve GO" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(1));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ decision: "GO", attestations: [], waivers: [{ pack: "onhand", reason: "posted manually" }], version: 2 });
  });

  it("approves a GO as the first approver with attestations and shows the service's refusal", async () => {
    let refuse = true;
    handler = (url, init) => {
      if (url.pathname !== goPath) return undefined;
      if (init.method === "POST") {
        if (refuse) return json(422, { code: "MIG-0001", detail: "CUT-1607 GO refused, checklist items not met: packs_reconciled" });
        return json(202, { pendingGo, plan: planAfter });
      }
      return json(200, readiness());
    };
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    await screen.findByRole("button", { name: "Record decision" });
    await userEvent.selectOptions(screen.getByLabelText("Decision"), "GO");
    expect(screen.getByText(/Yours is recorded as the first approval/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Attested load_test" }));
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText(/Every confirmed item needs a reference/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Evidence for Attested load_test"), { target: { value: "https://e/load" } });
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    let dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("second, different migration.cutover.manage");
    await userEvent.click(within(dialog).getByRole("button", { name: "Approve GO" }));
    expect(await screen.findByText(/CUT-1607 GO refused/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      decision: "GO", attestations: [{ code: "load_test", confirmed: true, reference: "https://e/load" }], version: 2,
    });
    // The form keeps its values after a refusal, so the approval can be sent again.
    refuse = false;
    await userEvent.click(screen.getByRole("button", { name: "Record decision" }));
    dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Approve GO" }));
    expect(await screen.findByText(/Your approval of the GO is recorded/)).toBeInTheDocument();
  });

  it("lets a second migration.cutover.manage holder approve a pending GO with its evidence", async () => {
    const attestedMet = items("MET").map((i) => (i.source === "attested" ? { ...i, status: "MET" as const, detail: "Confirmed: https://e/load" } : i));
    const go: GoNoGoDecision = { ...noGo, decision: "GO", approvers: ["alice", "bob"], decidedBy: "bob", reason: undefined, planVersion: 3, checklist: attestedMet };
    let decided = false;
    handler = (url, init) => {
      if (url.pathname !== goPath) return undefined;
      if (init.method === "POST") {
        decided = true;
        return json(201, { decision: go, plan: planAfter });
      }
      return json(200, decided ? readiness({ version: 3, items: attestedMet, decisions: [go] }) : readiness({ version: 2, systemReady: true, items: attestedMet, openItems: [], pendingGo }));
    };
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(await screen.findByText("GO waiting for a second approver")).toBeInTheDocument();
    expect(screen.getByText(/alice approved a GO/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Decision"), "GO");
    expect(screen.queryByRole("checkbox", { name: "Attested load_test" })).not.toBeInTheDocument();
    expect(screen.getByText(/You approve the GO alice approved/)).toBeInTheDocument();
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
    await userEvent.click(screen.getByRole("button", { name: "Approve GO" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("with alice and you as approvers");
    await userEvent.click(within(dialog).getByRole("button", { name: "Record GO" }));
    expect(await screen.findByText("GO recorded with alice and bob as approvers.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ decision: "GO", version: 2 });
    expect(await screen.findByText("A GO decision is recorded for this plan.")).toBeInTheDocument();
  });

  it("offers no form once a GO is recorded or the plan is rolled back", async () => {
    handler = () => json(200, readiness({ decisions: [{ ...noGo, seq: 2, decision: "GO", reason: undefined }] }));
    const first = renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(await screen.findByText("A GO decision is recorded for this plan.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record decision" })).not.toBeInTheDocument();
    first.unmount();
    handler = () => json(200, readiness({ rolledBack: true }));
    renderAt(`/migration-factory/cutover/${PLAN}/go-no-go`);
    expect(await screen.findByText("This plan was rolled back")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record decision" })).not.toBeInTheDocument();
  });

  it("is linked from the runbook, with the latest decision", async () => {
    handler = (url) =>
      url.pathname === `/api/v1/cutover/plans/${PLAN}`
        ? json(200, { ...planAfter, steps: [], rollbackSteps: [], decision: { seq: 1, decision: "NO_GO", approvers: ["cfo"], decidedAt: "2027-01-01T20:00:00Z" } })
        : undefined;
    renderAt(`/migration-factory/cutover/${PLAN}`);
    expect(await screen.findByText(/Latest decision: No-go by cfo/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the go/no-go decision" })).toHaveAttribute("href", `/migration-factory/cutover/${PLAN}/go-no-go`);
  });
});

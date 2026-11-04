import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { CutoverPlan, RunbookStep } from "./api/client";
import migrationFactory from "./module";
import { validatePlan } from "./NewCutoverPlanPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const PLAN = "0195f0a0-0000-7000-8000-0000000000c1";

const step = (seq: number, name: string, offsetMin: number, extra: Partial<RunbookStep> = {}): RunbookStep => ({
  seq,
  offsetMin,
  name,
  owner: "Migration engineer",
  plannedDurationMin: 60,
  actualDurationMin: 0,
  status: "PENDING",
  goNoGo: false,
  ...extra,
});

const expanded: CutoverPlan = {
  planId: PLAN,
  t0: "2027-01-01T00:00:00Z",
  packs: ["open_items"],
  entities: ["USMF", "DEMF"],
  abortOffsetMin: 2160,
  rolledBack: false,
  approvers: [],
  version: 1,
  createdAt: "2026-12-01T00:00:00Z",
  stepsDone: 0,
  stepsTotal: 4,
  openGoNoGo: [],
  abortDue: false,
  steps: [
    step(1, "Reconciliation complete", 1440),
    step(2, "Load pack open_items for USMF", 720, { pack: "open_items", entity: "USMF" }),
    step(3, "Load pack open_items for DEMF", 720, { pack: "open_items", entity: "DEMF" }),
    step(4, "Controller sign-off DEMF", 1380, { entity: "DEMF" }),
  ],
};

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

afterEach(() => {
  vi.unstubAllGlobals();
});

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

describe("per-entity runbook (D5a)", () => {
  it("sends expandByEntity only when the box is ticked", () => {
    const base = { t0: "2027-01-01T00:00", packs: ["open_items" as const], entities: "USMF", abortOffsetHours: "", rehearsalPlanId: "" };
    expect(validatePlan(base).input).not.toHaveProperty("expandByEntity");
    expect(validatePlan({ ...base, expandByEntity: true }).input?.expandByEntity).toBe(true);
    const many = Array.from({ length: 51 }, (_, i) => `E${i}`).join(", ");
    expect(validatePlan({ ...base, entities: many, expandByEntity: true }).errors.entities).toMatch(/at most 50/);
    expect(validatePlan({ ...base, entities: many }).errors.entities).toBeUndefined();
  });

  it("generates the expanded plan from the form", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/cutover/plans/generate")) return json(201, expanded);
      if (url.pathname === `/api/v1/cutover/plans/${PLAN}`) return json(200, expanded);
      return undefined;
    };
    const { container } = renderAt("/migration-factory/cutover/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("T-0 (UTC)"), { target: { value: "2027-01-01T00:00" } });
    fireEvent.change(screen.getByLabelText("Legal entities"), { target: { value: "USMF, DEMF" } });
    fireEvent.click(screen.getByLabelText("Expand the runbook per legal entity"));
    fireEvent.click(screen.getByRole("button", { name: "Generate plan" }));
    expect(await screen.findByRole("heading", { name: "Cutover runbook" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ t0: "2027-01-01T00:00:00Z", packs: ["open_items"], entities: ["USMF", "DEMF"], expandByEntity: true });
  });

  it("shows the entity of each step and filters the runbook by entity", async () => {
    handler = () => json(200, expanded);
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}`);
    const table = await findTable({ name: "Runbook steps" });
    await waitFor(() => expect(table).toHaveTextContent("Load pack open_items for USMF"));
    expect(within(table).getByRole("columnheader", { name: "Entity" })).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(5);
    fireEvent.change(screen.getByLabelText(/Show steps for/), { target: { value: "DEMF" } });
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(table).not.toHaveTextContent("for USMF");
    fireEvent.change(screen.getByLabelText(/Show steps for/), { target: { value: "__plan__" } });
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(table).toHaveTextContent("Reconciliation complete");
    expect(pageViolations(container)).toEqual([]);
  });

  it("has no entity column or filter on a plan-wide runbook", async () => {
    handler = () => json(200, { ...expanded, steps: [step(1, "Reconciliation complete", 1440)] });
    renderAt(`/migration-factory/cutover/${PLAN}`);
    const table = await findTable({ name: "Runbook steps" });
    await waitFor(() => expect(table).toHaveTextContent("Reconciliation complete"));
    expect(within(table).queryByRole("columnheader", { name: "Entity" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Show steps for/)).not.toBeInTheDocument();
  });
});

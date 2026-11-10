import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Procedure, ProcedureStep } from "./api/client";
import { validateProcedure } from "./NewProcedurePage";
import { referenceSteps, validateSteps } from "./ProcedureStepsEditor";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const P1 = "0190f0a0-0000-7000-8000-000000000d01";
const P2 = "0190f0a0-0000-7000-8000-000000000d02";

const draft: Procedure = { id: P2, legalEntityId: LE, name: "AX_STANDARD", version: 2, status: "DRAFT", revision: 1, steps: referenceSteps() };
const active: Procedure = { ...draft, id: P1, version: 1, status: "ACTIVE", steps: referenceSteps().slice(0, 2) };

function step(stepNo: number, conditionType: ProcedureStep["conditionType"], patch: Partial<ProcedureStep> = {}): ProcedureStep {
  return { stepNo, conditionType, requirementCel: "", exclusionGroup: "", exclusionMode: "", statistical: false, accountKey: "", ...patch };
}

function fill(label: string, value: string) {
  const field = screen.queryByLabelText(label) ?? screen.getByLabelText(new RegExp(`^${label}`));
  fireEvent.change(field, { target: { value } });
}

describe("procedure step rules", () => {
  it("accepts the reference procedure and an exclusion group with one mode", () => {
    expect(validateSteps(referenceSteps())).toEqual({});
    expect(validateSteps([step(10, "PRICE"), step(20, "LINE_DISC", { exclusionGroup: "EG1", exclusionMode: "BEST" }), step(30, "MULTILINE_DISC", { exclusionGroup: "EG1", exclusionMode: "BEST" })])).toEqual({});
  });

  it("names the failing step and field as the service does", () => {
    expect(validateSteps([])).toEqual({ steps: "Add at least one step." });
    expect(validateSteps([step(20, "PRICE"), step(20, "LINE_DISC")])).toHaveProperty(["steps[1].stepNo"]);
    expect(validateSteps([step(5, "PRICE")])).toHaveProperty(["steps[0].stepNo"]);
    expect(validateSteps([step(10, "LINE_DISC")])).toHaveProperty("steps", "Use exactly one Price step.");
    expect(validateSteps([step(10, "PRICE", { exclusionGroup: "EG1", exclusionMode: "BEST" })])).toHaveProperty(["steps[0].exclusionGroup"]);
    expect(validateSteps([step(10, "PRICE"), step(20, "LINE_DISC", { exclusionGroup: "EG1" })])).toHaveProperty(["steps[1].exclusionMode"]);
    expect(
      validateSteps([step(10, "PRICE"), step(20, "LINE_DISC", { exclusionGroup: "EG1", exclusionMode: "BEST" }), step(30, "TOTAL_DISC", { exclusionGroup: "EG1", exclusionMode: "FIRST" })]),
    ).toHaveProperty(["steps[2].exclusionMode"], "Group EG1 already uses BEST.");
    expect(validateSteps([step(10, "PRICE", { requirementCel: "x".repeat(513) })])).toHaveProperty(["steps[0].requirementCel"]);
    expect(validateSteps([step(10, "PRICE"), step(20, "LINE_DISC"), step(30, "LINE_DISC", { exclusionGroup: "EG1", exclusionMode: "BEST" })])).toEqual({
      "steps[2].conditionType": "This condition type is already step 20; use each type once.",
    });
    expect(validateProcedure({ legalEntityId: "x", name: "ax standard", steps: referenceSteps() })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      name: "Use upper-case letters, digits, _ or -, such as AX_STANDARD.",
    });
  });
});

describe("Pricing procedures", () => {
  it("lists versions with status and step order and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/procedures")) return json(200, { items: [draft, active] });
      if (url.pathname.endsWith(`/procedures/${P1}`)) return json(200, active);
      return undefined;
    };
    const { container } = renderAt("/pricing/procedures");
    const table = await findTable({ name: "Pricing procedures" });
    await waitFor(() => expect(table).toHaveTextContent("AX_STANDARD"));
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("Draft");
    expect(rows[1]).toHaveTextContent("Price → Line discount → Multiline discount → Total discount → Surcharge");
    expect(rows[2]).toHaveTextContent("Active");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(rows[2]).getByRole("button", { name: "AX_STANDARD" }));
    expect(await screen.findByRole("heading", { name: "AX_STANDARD version 1" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/procedures");
    expect(await screen.findByText("No procedures")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/pricing/procedures");
    expect(await findAlert()).toHaveTextContent("internal error (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New pricing procedure", () => {
  it("starts from the reference steps and creates a draft with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/procedures")) return json(201, { ...draft, id: P2 });
      if (url.pathname.endsWith(`/procedures/${P2}`)) return json(200, draft);
      return undefined;
    };
    const { container } = renderAt("/pricing/procedures/new");
    expect(screen.getAllByRole("group")).toHaveLength(5);
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Add step" }));
    expect(screen.getAllByRole("group")).toHaveLength(6);
    const groups = screen.getAllByLabelText(/^Exclusion group/);
    fireEvent.change(groups[5], { target: { value: "EG1" } });
    fireEvent.change(screen.getAllByLabelText("Exclusion mode")[5], { target: { value: "FIRST" } });
    fireEvent.change(screen.getAllByLabelText(/^Requirement/)[5], { target: { value: "  line.qty > 10  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByRole("heading", { name: "AX_STANDARD version 2" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    const body = post.body as { legalEntityId: string; name: string; steps: ProcedureStep[] };
    expect(body.legalEntityId).toBe(LE);
    expect(body.name).toBe("AX_STANDARD");
    expect(body.steps.map((s) => s.stepNo)).toEqual([10, 20, 30, 40, 50, 60]);
    // The new step starts at the first condition type the procedure does not use yet.
    expect(body.steps[5]).toMatchObject({ conditionType: "FREIGHT", exclusionGroup: "EG1", exclusionMode: "FIRST", requirementCel: "line.qty > 10" });
    expect(idempotencyKey(post)).toBeTruthy();
  });

  it("blocks invalid steps before calling the service and removes a step", async () => {
    renderAt("/pricing/procedures/new");
    fill("Legal entity", LE);
    fireEvent.change(screen.getAllByLabelText("Step number")[1], { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Use a step number from 10 to 990.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Remove step 5" }));
    expect(screen.getAllByRole("group")).toHaveLength(4);
  });

  it("copies an existing version into a new one", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/procedures/${P1}`) ? json(200, active) : undefined);
    renderAt(`/pricing/procedures/new?from=${P1}`);
    expect(await screen.findByRole("heading", { name: "New procedure version" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByRole("group")).toHaveLength(2));
    expect(screen.getByLabelText("Legal entity")).toHaveAttribute("data-value", LE);
  });
});

describe("Procedure detail", () => {
  it("saves a changed draft at its revision, then activates it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/procedures/${P2}:revise`)) {
        const body = JSON.parse(String(init.body)) as { steps: ProcedureStep[] };
        return json(200, { ...draft, revision: 2, steps: body.steps });
      }
      if (init.method === "POST" && url.pathname.endsWith(`/procedures/${P2}:activate`)) return json(200, { ...draft, revision: 2, status: "ACTIVE", steps: referenceSteps().slice(0, 4) });
      if (url.pathname.endsWith(`/procedures/${P2}`)) return json(200, draft);
      return undefined;
    };
    const { container } = renderAt(`/pricing/procedures/${P2}`);
    expect(await screen.findByRole("heading", { name: "AX_STANDARD version 2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Remove step 50" }));
    fireEvent.click(screen.getByRole("button", { name: "Activate version 2" }));
    expect(await findAlert()).toHaveTextContent("The steps have unsaved changes.");

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await findStatus()).toHaveTextContent("Draft saved (revision 2).");
    const revise = server.calls.find((c) => c.url.pathname.endsWith(":revise"))!;
    expect((revise.body as { revision: number; steps: ProcedureStep[] }).revision).toBe(1);
    expect((revise.body as { steps: ProcedureStep[] }).steps).toHaveLength(4);
    expect(idempotencyKey(revise)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Activate version 2" }));
    expect(await findTable({ name: "Procedure steps" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Version 2 of AX_STANDARD is active");
    expect(screen.queryByRole("button", { name: "Activate version 2" })).toBeNull();
    expect(idempotencyKey(server.calls.find((c) => c.url.pathname.endsWith(":activate"))!)).toBeTruthy();
  });

  it("shows an active version read-only with a link to a new version", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/procedures/${P1}`) ? json(200, { ...active, steps: [step(10, "PRICE"), step(20, "LINE_DISC", { exclusionGroup: "EG1", exclusionMode: "FIRST", requirementCel: "line.qty > 1" })] }) : undefined);
    renderAt(`/pricing/procedures/${P1}`);
    const table = await findTable({ name: "Procedure steps" });
    expect(table).toHaveTextContent("EG1 (first)");
    expect(table).toHaveTextContent("line.qty > 1");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
    expect(screen.getByRole("link", { name: "New version from this" })).toHaveAttribute("href", `/pricing/procedures/new?from=${P1}`);
  });

  it("explains PRC-0008 when the version was activated elsewhere", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "PRC-0008", detail: "procedure AX_STANDARD version 2 is ACTIVE and immutable; create a new version" });
      if (url.pathname.endsWith(`/procedures/${P2}`)) return json(200, draft);
      return undefined;
    };
    renderAt(`/pricing/procedures/${P2}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate version 2" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("This version can no longer change");
    expect(alert).toHaveTextContent("(PRC-0008)");
  });

  it("shows the error state when the version is unknown", async () => {
    server.handler = () => json(404, { code: "PRC-0042", detail: "no pricing procedure x" });
    renderAt(`/pricing/procedures/${P2}`);
    expect(await findAlert()).toHaveTextContent("(PRC-0042)");
  });
});

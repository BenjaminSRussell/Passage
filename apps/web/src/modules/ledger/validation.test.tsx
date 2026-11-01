import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findTable } from "../../test-utils";
import type { ValidationRule } from "./api/client";
import { emptyRule, formatCondition, parseConditions, toRuleDraft, toRuleInput, validateRule } from "./ValidationRuleFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const RULE_ID = "0190a000-0000-7000-8000-0000000000d1";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}`;

const rule: ValidationRule = {
  id: RULE_ID,
  ledgerId: LEDGER,
  sequence: 10,
  name: "Department on large expenses",
  level: "line",
  severity: "error",
  prerequisite: [
    { field: "ACCOUNT", op: "between", values: ["6000", "6999"] },
    { field: "AMOUNT", op: "gt", values: ["1000"] },
  ],
  checks: [{ field: "DEPT", op: "notblank" }],
  message: "Department is required above 1,000",
  active: true,
  lockVersion: 3,
};
const warning: ValidationRule = { ...rule, id: "0190a000-0000-7000-8000-0000000000d2", name: "Round amounts", severity: "warning", active: false, sequence: 20 };

function fake(items: ValidationRule[] = [rule, warning]) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === `${BASE}/validation-rules` && method === "GET") return json(200, { items });
    const m = url.pathname.match(/\/validation-rules\/([^/]+)$/);
    if (m && method === "GET") return json(200, items.find((r) => r.id === m[1]) ?? rule);
    if (m && method === "PUT") return json(200, { ...rule, ...JSON.parse(String(init.body)), id: m[1], lockVersion: 4 });
    return undefined;
  };
}

describe("validation rule helpers", () => {
  it("parses conditions one per line and names the first bad line", () => {
    expect(parseConditions("account between 6000, 6999\n\nAMOUNT GT 1000\nDEPT notblank").conditions).toEqual([
      { field: "ACCOUNT", op: "between", values: ["6000", "6999"] },
      { field: "AMOUNT", op: "gt", values: ["1000"] },
      { field: "DEPT", op: "notblank" },
    ]);
    expect(parseConditions("AMOUNT").error).toContain("Line 1");
    expect(parseConditions("DEPT notblank\nAMOUNT near 5").error).toContain("Line 2");
    expect(parseConditions("DEPT blank x").error).toContain("takes no values");
    expect(parseConditions("ACCOUNT between 6000").error).toContain("lower and an upper");
    expect(parseConditions("DEPT eq").error).toContain("exactly one value");
    expect(parseConditions("DEPT in").error).toContain("1 to 20");
    expect(formatCondition(rule.prerequisite[0])).toBe("ACCOUNT between 6000, 6999");
    expect(formatCondition(rule.checks[0])).toBe("DEPT notblank");
  });

  it("round-trips a rule and validates the form", () => {
    expect(toRuleInput(toRuleDraft(rule))).toEqual({
      sequence: 10, name: rule.name, level: "line", severity: "error", prerequisite: rule.prerequisite, checks: rule.checks, message: rule.message, active: true,
    });
    expect(validateRule(toRuleDraft(rule))).toEqual({});
    expect(validateRule(emptyRule)).toMatchObject({ name: expect.any(String), message: expect.any(String), checks: "Add at least one check." });
    expect(validateRule({ ...toRuleDraft(rule), sequence: "0" }).sequence).toBeDefined();
    expect(validateRule({ ...toRuleDraft(rule), checks: "DEPT zz" }).checks).toContain("operator");
    expect(validateRule({ ...toRuleDraft(rule), prerequisite: "AMOUNT" }).prerequisite).toContain("Line 1");
  });
});

describe("validation rules list", () => {
  it("asks for a ledger first, then lists the rules and opens one", async () => {
    fake();
    const { container } = renderAt("/ledger/validation-rules");
    expect(screen.getByText("Enter a ledger id to see its validation rules.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Show ledger" }));
    const grid = await findTable({ name: "Validation rules" });
    const open = await screen.findByRole("button", { name: rule.name });
    const row = open.closest("tr") as HTMLElement;
    expect(row.textContent).toContain("Refuses");
    expect(row.textContent).toContain("Each line");
    expect(row.textContent).toContain("Active");
    expect((screen.getByRole("button", { name: warning.name }).closest("tr") as HTMLElement).textContent).toMatch(/Warns.*Off/);
    expect(grid).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(open);
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/validation-rules/${RULE_ID}`);
    expect(await screen.findByLabelText("Name")).toHaveValue(rule.name);
    expect(screen.getByLabelText("Only when")).toHaveValue("ACCOUNT between 6000, 6999\nAMOUNT gt 1000");
    expect(screen.getByLabelText("Must hold")).toHaveValue("DEPT notblank");
  });

  it("shows the empty state", async () => {
    fake([]);
    renderAt(`/ledger/validation-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("No validation rules yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [rule] }));
    renderAt(`/ledger/validation-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the validation rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: rule.name })).toBeInTheDocument();
  });
});

describe("validation rule form", () => {
  it("creates a rule and sends the conditions in the body", async () => {
    fake();
    const { container } = renderAt(`/ledger/validation-rules/new?ledger=${LEDGER}`);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Dept on expenses" } });
    fireEvent.change(screen.getByLabelText("Only when"), { target: { value: "ACCOUNT between 6000, 6999\nAMOUNT gt 1000" } });
    fireEvent.change(screen.getByLabelText("Must hold"), { target: { value: "DEPT notblank" } });
    fireEvent.change(screen.getByLabelText("When the check fails"), { target: { value: "warning" } });
    fireEvent.change(screen.getByLabelText("Message"), { target: { value: "Needs a department" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save validation rule" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/validation-rules"));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toMatch(new RegExp(`^${BASE}/validation-rules/[0-9a-f-]{36}$`));
    expect(new Headers(put.init.headers).get("If-Match")).toBeNull();
    expect(put.body).toEqual({
      sequence: 10, name: "Dept on expenses", level: "line", severity: "warning", prerequisite: rule.prerequisite, checks: rule.checks, message: "Needs a department", active: true,
    });
  });

  it("replaces at the lock version and shows service errors", async () => {
    fake();
    renderAt(`/ledger/validation-rules/${RULE_ID}?ledger=${LEDGER}`);
    await screen.findByDisplayValue(rule.name);
    server.handler = (_url, init) => ((init.method ?? "GET") === "PUT" ? json(422, { code: "LEDGER_VALIDATION_RULE_INVALID", detail: "field \"NOPE\" is not a header field" }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Save validation rule" }));
    expect(await screen.findByText(/is not a header field/)).toBeInTheDocument();
    expect(new Headers(server.calls.find((c) => c.method === "PUT")!.init.headers).get("If-Match")).toBe('"3"');
  });

  it("blocks a bad form before any request, needs a ledger and shows a load error", async () => {
    fake();
    renderAt(`/ledger/validation-rules/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save validation rule" }));
    expect(await screen.findByText("Enter a name of up to 80 characters.")).toBeInTheDocument();
    expect(screen.getByText("Add at least one check.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("explains how to reach the form without a ledger", () => {
    renderAt("/ledger/validation-rules/new");
    expect(screen.getByText(/Open this page from the validation rules list/)).toBeInTheDocument();
  });

  it("shows a rule that cannot be loaded", async () => {
    server.handler = () => json(404, { code: "LEDGER_VALIDATION_RULE_NOT_FOUND", detail: "validation rule not found" });
    renderAt(`/ledger/validation-rules/${RULE_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the validation rule")).toBeInTheDocument();
  });
});

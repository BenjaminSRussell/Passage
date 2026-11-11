import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findTable } from "../../test-utils";
import type { AdvancedRule, Structure } from "./api/client";
import { emptyAdvancedRule, formatRuleConstraint, parseRuleConstraints, toAdvancedRuleDraft, toAdvancedRuleInput, validateAdvancedRule } from "./AdvancedRuleFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const DRAFT = "0190a000-0000-7000-8000-0000000000a1";
const ACTIVE = "0190a000-0000-7000-8000-0000000000a2";
const RULE_ID = "0190a000-0000-7000-8000-0000000000b1";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}`;

const structure = (id: string, version: number, status: Structure["status"]): Structure => ({
  id, ledgerId: LEDGER, name: "PL", version, priority: 0, effectiveFrom: "2026-01-01", status, segments: ["DEPT"], accountFilter: ["5000..5999"], constraints: [],
});

const rule: AdvancedRule = {
  id: RULE_ID,
  ledgerId: LEDGER,
  structureId: DRAFT,
  name: "PROJECT_ON_D100",
  priority: 1,
  accounts: ["5000..5999"],
  dimensions: { DEPT: "D100" },
  segments: ["PROJECT"],
  constraints: [{ segment: "PROJECT", allowed: ["*"] }],
  lockVersion: 3,
};

function fake(items: AdvancedRule[] = [rule], structures: Structure[] = [structure(DRAFT, 2, "draft"), structure(ACTIVE, 1, "active")]) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === `${BASE}/advanced-rules` && method === "GET") return json(200, { items });
    if (url.pathname === `${BASE}/account-structures` && method === "GET") return json(200, { items: structures });
    const m = url.pathname.match(/\/advanced-rules\/([^/]+)$/);
    if (m && method === "GET") return json(200, items.find((r) => r.id === m[1]) ?? rule);
    if (m && method === "PUT") return json(200, { ...rule, ...JSON.parse(String(init.body)), id: m[1], lockVersion: 4 });
    return undefined;
  };
}

describe("advanced rule helpers", () => {
  it("parses constraint rows and names the first bad line", () => {
    expect(parseRuleConstraints("project: *\nPROJECT under MAIN_ACCOUNT=5000..5999, DEPT=D100: P1, blank").rows).toEqual([
      { segment: "PROJECT", allowed: ["*"] },
      { segment: "PROJECT", under: { MAIN_ACCOUNT: "5000..5999", DEPT: "D100" }, allowed: ["P1", "blank"] },
    ]);
    expect(parseRuleConstraints("PROJECT").error).toContain("Line 1");
    expect(parseRuleConstraints("PROJECT: *\nPROJECT under DEPT: *").error).toContain("Line 2");
    expect(parseRuleConstraints("PROJECT:").error).toContain("at least one allowed value");
    expect(parseRuleConstraints("PROJECT: a b").error).toContain("exact values");
    expect(formatRuleConstraint(rule.constraints[0])).toBe("PROJECT: *");
  });

  it("round-trips a rule and validates the form", () => {
    expect(toAdvancedRuleInput(toAdvancedRuleDraft(rule))).toEqual({
      structureId: DRAFT, name: rule.name, priority: 1, accounts: rule.accounts, dimensions: rule.dimensions, segments: rule.segments, constraints: rule.constraints,
    });
    expect(validateAdvancedRule(toAdvancedRuleDraft(rule))).toEqual({});
    expect(validateAdvancedRule(emptyAdvancedRule)).toMatchObject({ structureId: expect.any(String), name: expect.any(String), segments: "Add at least one dimension." });
    const draft = toAdvancedRuleDraft(rule);
    expect(validateAdvancedRule({ ...draft, accounts: "", dimensions: "" }).accounts).toContain("at least one");
    expect(validateAdvancedRule({ ...draft, accounts: "blank" }).accounts).toBeDefined();
    expect(validateAdvancedRule({ ...draft, dimensions: "DEPT" }).dimensions).toBeDefined();
    expect(validateAdvancedRule({ ...draft, dimensions: "dept=D1" }).dimensions).toBeDefined();
    expect(validateAdvancedRule({ ...draft, segments: "PROJECT, PROJECT" }).segments).toBeDefined();
    expect(validateAdvancedRule({ ...draft, priority: "-1" }).priority).toBeDefined();
    expect(validateAdvancedRule({ ...draft, constraints: "CC: *" }).constraints).toContain("only a dimension this rule adds");
  });
});

describe("advanced rules list", () => {
  it("asks for a ledger first, then lists the rules with their structure and opens one", async () => {
    fake();
    const { container } = renderAt("/ledger/advanced-rules");
    expect(screen.getByText("Choose a ledger to see its advanced rules.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    const grid = await findTable({ name: "Advanced rules" });
    const open = await screen.findByRole("button", { name: rule.name });
    await waitFor(() => expect((open.closest("tr") as HTMLElement).textContent).toContain("PL v2"));
    const row = open.closest("tr") as HTMLElement;
    expect(row.textContent).toContain("draft");
    expect(row.textContent).toContain("accounts 5000..5999 and DEPT = D100");
    expect(row.textContent).toContain("PROJECT");
    expect(grid).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(open);
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/advanced-rules/${RULE_ID}`);
    expect(await screen.findByLabelText("Name")).toHaveValue(rule.name);
    expect(screen.getByLabelText("Dimension conditions")).toHaveValue("DEPT=D100");
    expect(screen.getByLabelText("Allowed values")).toHaveValue("PROJECT: *");
  });

  it("shows the empty state", async () => {
    fake([]);
    renderAt(`/ledger/advanced-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("No advanced rules yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = (url) =>
      url.pathname.endsWith("/account-structures") ? json(200, { items: [] }) : fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [rule] });
    renderAt(`/ledger/advanced-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the advanced rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: rule.name })).toBeInTheDocument();
  });
});

describe("advanced rule form", () => {
  it("creates a rule on a draft structure version and sends the body", async () => {
    fake();
    const { container } = renderAt(`/ledger/advanced-rules/new?ledger=${LEDGER}`);
    const select = await screen.findByLabelText("Account structure version");
    await screen.findByRole("option", { name: "PL v2 (draft)" });
    expect(screen.queryByRole("option", { name: /PL v1/ })).toBeNull();
    fireEvent.change(select, { target: { value: DRAFT } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "PROJECT_ON_D100" } });
    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Accounts"), { target: { value: "5000..5999" } });
    fireEvent.change(screen.getByLabelText("Dimension conditions"), { target: { value: "DEPT=D100" } });
    fireEvent.change(screen.getByLabelText("Dimensions added"), { target: { value: "PROJECT" } });
    fireEvent.change(screen.getByLabelText("Allowed values"), { target: { value: "PROJECT: *" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save advanced rule" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/advanced-rules"));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toMatch(new RegExp(`^${BASE}/advanced-rules/[0-9a-f-]{36}$`));
    expect(new Headers(put.init.headers).get("If-Match")).toBeNull();
    expect(put.body).toEqual({
      structureId: DRAFT, name: "PROJECT_ON_D100", priority: 1, accounts: ["5000..5999"], dimensions: { DEPT: "D100" }, segments: ["PROJECT"], constraints: [{ segment: "PROJECT", allowed: ["*"] }],
    });
  });

  it("replaces at the lock version and shows service errors", async () => {
    fake();
    renderAt(`/ledger/advanced-rules/${RULE_ID}?ledger=${LEDGER}`);
    await screen.findByDisplayValue(rule.name);
    server.handler = (_url, init) => ((init.method ?? "GET") === "PUT" ? json(422, { code: "LEDGER_ADVANCED_RULE_INVALID", detail: "segment DEPT is already a segment of structure PL" }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Save advanced rule" }));
    expect(await screen.findByText(/already a segment of structure PL/)).toBeInTheDocument();
    expect(new Headers(server.calls.find((c) => c.method === "PUT")!.init.headers).get("If-Match")).toBe('"3"');
  });

  it("locks the form when the structure version is no longer a draft", async () => {
    fake([{ ...rule, structureId: ACTIVE }]);
    renderAt(`/ledger/advanced-rules/${RULE_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText("This structure version is no longer a draft")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save advanced rule" })).toBeDisabled();
  });

  it("blocks a bad form before any request, needs a ledger and shows a load error", async () => {
    fake();
    renderAt(`/ledger/advanced-rules/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save advanced rule" }));
    expect(await screen.findByText("Choose the draft account structure version this rule belongs to.")).toBeInTheDocument();
    expect(screen.getByText("Add at least one dimension.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(0);
  });

  it("points to the structures when no draft version can take a rule, and shows a structures load error", async () => {
    fake([rule], [structure(ACTIVE, 1, "active")]);
    const { unmount } = renderAt(`/ledger/advanced-rules/new?ledger=${LEDGER}`);
    expect(await screen.findByText("No draft account structure version")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create a draft version" })).toHaveAttribute("href", `/ledger/structures?ledger=${LEDGER}`);
    unmount();
    server.handler = (url) => (url.pathname.endsWith("/account-structures") ? json(500, { code: "INTERNAL", detail: "boom" }) : undefined);
    renderAt(`/ledger/advanced-rules/new?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the account structures")).toBeInTheDocument();
  });

  it("explains how to reach the form without a ledger", () => {
    renderAt("/ledger/advanced-rules/new");
    expect(screen.getByText(/Open this page from the advanced rules list/)).toBeInTheDocument();
  });

  it("shows a rule that cannot be loaded", async () => {
    server.handler = () => json(404, { code: "LEDGER_ADVANCED_RULE_NOT_FOUND", detail: "advanced rule not found" });
    renderAt(`/ledger/advanced-rules/${RULE_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the advanced rule")).toBeInTheDocument();
  });
});

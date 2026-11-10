import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SplittingRule } from "./api/client";
import { parseDefaults, toSplittingDraft, toSplittingInput, validateSplitting } from "./SplittingRuleFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const RULE_ID = "0190a000-0000-7000-8000-0000000000d1";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}`;

const rule: SplittingRule = {
  id: RULE_ID,
  ledgerId: LEDGER,
  sourceType: "vendor_invoice",
  splittingDimensions: ["PROFIT_CENTER"],
  zeroBalanceClearingAccount: "199999",
  defaultSplittingValue: { PROFIT_CENTER: "PC9" },
  active: true,
  version: 3,
};

function fake(rules: SplittingRule[] = [rule]) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === `${BASE}/splitting-rules` && method === "GET") return json(200, { items: rules });
    const m = url.pathname.match(/\/splitting-rules\/([^/]+)$/);
    if (m && method === "GET") return json(200, rules.find((x) => x.id === m[1]) ?? rule);
    if (m && method === "PUT") return json(200, { ...rule, ...JSON.parse(String(init.body)), id: m[1], version: 4 });
    return undefined;
  };
}

describe("splitting rule helpers", () => {
  it("parses default values and reports a malformed pair", () => {
    expect(parseDefaults("PROFIT_CENTER=PC9, SEGMENT = S0").values).toEqual({ PROFIT_CENTER: "PC9", SEGMENT: "S0" });
    expect(parseDefaults("PROFIT_CENTER").error).toContain("DIMENSION=value");
    expect(parseDefaults("A=b c").error).toContain("up to 20");
    expect(parseDefaults("").values).toEqual({});
  });

  it("round-trips a rule and validates the draft", () => {
    const draft = toSplittingDraft(rule);
    expect(draft).toEqual({ sourceType: "vendor_invoice", dimensions: "PROFIT_CENTER", clearingAccount: "199999", defaults: "PROFIT_CENTER=PC9", active: true });
    expect(toSplittingInput(draft)).toEqual({
      sourceType: "vendor_invoice",
      splittingDimensions: ["PROFIT_CENTER"],
      zeroBalanceClearingAccount: "199999",
      defaultSplittingValue: { PROFIT_CENTER: "PC9" },
      active: true,
    });
    expect(validateSplitting(draft)).toEqual({});
    expect(toSplittingInput({ ...draft, clearingAccount: " ", defaults: "" })).not.toHaveProperty("zeroBalanceClearingAccount");
    expect(validateSplitting({ ...draft, sourceType: " " }).sourceType).toBeDefined();
    expect(validateSplitting({ ...draft, dimensions: "" }).dimensions).toBeDefined();
    expect(validateSplitting({ ...draft, dimensions: "A, A" }).dimensions).toContain("twice");
    expect(validateSplitting({ ...draft, clearingAccount: "no good" }).clearingAccount).toBeDefined();
    expect(validateSplitting({ ...draft, defaults: "SEGMENT=S0" }).defaults).toContain("splitting dimensions");
  });
});

describe("splitting rules list", () => {
  it("asks for a ledger id first, then lists rules and opens one", async () => {
    fake();
    const { container } = renderAt("/ledger/splitting-rules");
    expect(screen.getByText("Enter a ledger id to see its splitting rules.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Show ledger" }));
    const open = await screen.findByRole("button", { name: "vendor_invoice" });
    const row = open.closest("tr") as HTMLElement;
    expect(row.textContent).toContain("PROFIT_CENTER");
    expect(row.textContent).toContain("199999");
    expect(row.textContent).toContain("Active");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(open);
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/splitting-rules/${RULE_ID}`);
    expect(await screen.findByLabelText("Source type")).toHaveValue("vendor_invoice");
    expect(screen.getByLabelText("Default values")).toHaveValue("PROFIT_CENTER=PC9");
  });

  it("shows the empty state", async () => {
    fake([]);
    renderAt(`/ledger/splitting-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("No splitting rules yet")).toBeInTheDocument();
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [rule] }));
    renderAt(`/ledger/splitting-rules?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the splitting rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "vendor_invoice" })).toBeInTheDocument();
  });
});

describe("splitting rule form", () => {
  it("creates a rule and sends it in the body", async () => {
    fake();
    const { container } = renderAt(`/ledger/splitting-rules/new?ledger=${LEDGER}`);
    fireEvent.change(screen.getByLabelText("Source type"), { target: { value: "vendor_invoice" } });
    fireEvent.change(screen.getByLabelText("Splitting dimensions"), { target: { value: "profit_center" } });
    fireEvent.change(screen.getByLabelText(/Zero-balance clearing account/), { target: { value: "199999" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save splitting rule" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/splitting-rules"));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toMatch(new RegExp(`^${BASE}/splitting-rules/[0-9a-f-]{36}$`));
    expect(new Headers(put.init.headers).get("If-Match")).toBeNull();
    expect(put.body).toEqual({ sourceType: "vendor_invoice", splittingDimensions: ["PROFIT_CENTER"], zeroBalanceClearingAccount: "199999", defaultSplittingValue: {}, active: true });
  });

  it("replaces at the rule version and shows service errors", async () => {
    fake();
    renderAt(`/ledger/splitting-rules/${RULE_ID}?ledger=${LEDGER}`);
    await screen.findByDisplayValue("vendor_invoice");
    server.handler = (_url, init) => ((init.method ?? "GET") === "PUT" ? json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the splitting rule changed since it was read" }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Save splitting rule" }));
    expect(await screen.findByText(/the splitting rule changed since it was read/)).toBeInTheDocument();
    expect(new Headers(server.calls.find((c) => c.method === "PUT")!.init.headers).get("If-Match")).toBe('"3"');
  });

  it("blocks a bad form before any request and needs a ledger", () => {
    fake();
    renderAt(`/ledger/splitting-rules/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save splitting rule" }));
    expect(screen.getByText("Enter the source type, up to 60 characters, such as vendor_invoice.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("asks to open the page from the list when no ledger is chosen", () => {
    fake();
    renderAt("/ledger/splitting-rules/new");
    expect(screen.getByText(/Open this page from the splitting rules list/)).toBeInTheDocument();
  });
});

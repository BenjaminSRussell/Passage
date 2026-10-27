import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { ACTIONS, toAccountInput, validateAccount, type AccountDraft } from "./BankAccounts";
import { toEntryInput, validateEntry, type EntryDraft } from "./CashForecast";
import { normalizeIban, validIban } from "./format";
import treasury from "./module";
import { MAX_BYTES, validateImport } from "./Statements";

const LE = "0190f0a0-0000-7000-8000-000000000001";
const GL = "0190f0a0-0000-7000-8000-0000000000a1";

const goodAccount: AccountDraft = {
  legalEntityId: LE,
  accountCode: "OPS-01",
  iban: "de89 3704 0044 0532 0130 00",
  bic: "cobadeffxxx",
  currency: "EUR",
  mainAccountId: GL,
};

describe("treasury module", () => {
  it("registers list, create and detail routes and is discovered by the shell", () => {
    expect(treasury.id).toBe("treasury");
    expect(treasury.routes.map((r) => r.path)).toEqual([
      "/treasury",
      "/treasury/bank-accounts/new",
      "/treasury/bank-accounts/:id",
      "/treasury/statements/import",
      "/treasury/statements/:id",
      "/treasury/cash-forecast",
    ]);
    const paths = new Set(treasury.routes.map((r) => r.path));
    expect(treasury.nav.every((n) => paths.has(n.to) && n.to.startsWith("/treasury"))).toBe(true);
    expect(treasury.routes.every((r) => r.element !== null && typeof r.handle?.title === "string")).toBe(true);
    expect(treasury.nav.find((n) => n.to === "/treasury/statements/import")?.duty).toBe("treasury.statement.import");
    expect(loadModules().some((m) => m.id === "treasury")).toBe(true);
  });
});

describe("IBAN rules", () => {
  it("accepts valid IBANs with spaces and lower case, as the service does", () => {
    expect(validIban("DE89370400440532013000")).toBe(true);
    expect(validIban("gb82 west 1234 5698 7654 32")).toBe(true);
    expect(normalizeIban("gb82 west 1234 5698 7654 32")).toBe("GB82WEST12345698765432");
  });

  it("rejects a wrong check digit and a malformed shape", () => {
    expect(validIban("DE88370400440532013000")).toBe(false);
    expect(validIban("DE89")).toBe(false);
    expect(validIban("1289370400440532013000")).toBe(false);
  });
});

describe("validateAccount", () => {
  it("passes a complete draft and normalizes it for the API", () => {
    expect(validateAccount(goodAccount)).toEqual({});
    expect(toAccountInput(goodAccount)).toEqual({
      legalEntityId: LE,
      accountCode: "OPS-01",
      iban: "DE89370400440532013000",
      bic: "COBADEFFXXX",
      currency: "EUR",
      mainAccountId: GL,
    });
    expect(toAccountInput({ ...goodAccount, bic: " " })).not.toHaveProperty("bic");
  });

  it("names every field that the service would refuse", () => {
    const errors = validateAccount({ legalEntityId: "x", accountCode: "ops 01", iban: "DE88370400440532013000", bic: "BAD", currency: "eur", mainAccountId: "" });
    expect(Object.keys(errors).sort()).toEqual(["accountCode", "bic", "currency", "iban", "legalEntityId", "mainAccountId"]);
  });
});

describe("account lifecycle actions", () => {
  it("offers only the transitions the service allows", () => {
    expect(ACTIONS.draft.map((a) => a.action)).toEqual(["activate"]);
    expect(ACTIONS.active.map((a) => a.action)).toEqual(["deactivate"]);
    expect(ACTIONS.inactive.map((a) => a.action)).toEqual(["activate", "close"]);
    expect(ACTIONS.closed).toEqual([]);
  });
});

describe("validateImport", () => {
  it("requires an account id and a file within the upload limit", () => {
    expect(validateImport({ bankAccountId: "", file: null })).toEqual({
      bankAccountId: "Enter the bank account id (a UUID).",
      file: "Choose the camt.053 file from the bank.",
    });
    const big = new File(["x"], "big.xml");
    Object.defineProperty(big, "size", { value: MAX_BYTES + 1 });
    expect(validateImport({ bankAccountId: LE, file: big }).file).toMatch(/larger than 50 MB/);
    expect(validateImport({ bankAccountId: LE, file: new File(["<x/>"], "s.xml") })).toEqual({});
  });
});

describe("forecast entries", () => {
  const draft: EntryDraft = { sourceType: "AP", sourceId: "", expectedDate: "2026-11-02", amount: "-250.50", currency: "EUR", confidence: "" };

  it("keeps the signed amount as a decimal string and omits optional fields", () => {
    expect(validateEntry(draft)).toEqual({});
    expect(toEntryInput(LE, draft)).toEqual({ legalEntityId: LE, sourceType: "AP", expectedDate: "2026-11-02", amount: "-250.50", currency: "EUR" });
    expect(toEntryInput(LE, { ...draft, confidence: "0.8", sourceId: "INV-1" })).toMatchObject({ confidence: "0.8", sourceId: "INV-1" });
  });

  it("refuses amounts with more than six places, confidence outside 0 to 1, and a missing date", () => {
    const errors = validateEntry({ ...draft, amount: "1.1234567", confidence: "1.5", expectedDate: "" });
    expect(Object.keys(errors).sort()).toEqual(["amount", "confidence", "expectedDate"]);
    expect(validateEntry({ ...draft, confidence: "1.0" })).toEqual({});
    expect(validateEntry({ ...draft, confidence: "0" })).toEqual({});
  });
});

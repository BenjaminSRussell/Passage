import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { ACTIONS, toAccountInput, validateAccount, type AccountDraft } from "./BankAccounts";
import { toEntryInput, validateEntry, type EntryDraft } from "./CashForecast";
import { CHECK_ACTIONS, toCheckInput, validateCheck } from "./Checks";
import { toInstrumentInput, validateFacility, validateInstrument, type InstrumentDraft } from "./Facilities";
import { fractionFromPercent, normalizeIban, percent, validAmount, validIban } from "./format";
import { toLoanInput, validateLoan, type LoanDraft } from "./Loans";
import treasury from "./module";
import { MAX_BYTES, validateImport } from "./Statements";

const LE = "0190f0a0-0000-7000-8000-000000000001";
const GL = "0190f0a0-0000-7000-8000-0000000000a1";

const goodAccount: AccountDraft = {
  legalEntityId: LE,
  accountCode: "OPS-01",
  iban: "de89 3704 0044 0532 0130 00",
  statementAccount: "",
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
      "/treasury/reconciliations",
      "/treasury/reconciliations/new",
      "/treasury/reconciliations/:id",
      "/treasury/cash-position",
      "/treasury/cash-forecast",
      "/treasury/checks",
      "/treasury/checks/new",
      "/treasury/checks/:id",
      "/treasury/facilities",
      "/treasury/facilities/new",
      "/treasury/facilities/:id",
      "/treasury/loans",
      "/treasury/loans/new",
      "/treasury/loans/:id",
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
    expect(toAccountInput(goodAccount)).not.toHaveProperty("statementAccount");
    expect(toAccountInput({ ...goodAccount, statementAccount: "3704 0044/0532013000" }).statementAccount).toBe("37040044/0532013000");
  });

  it("names every field that the service would refuse", () => {
    const errors = validateAccount({
      legalEntityId: "x",
      accountCode: "ops 01",
      iban: "DE88370400440532013000",
      statementAccount: "1".repeat(36),
      bic: "BAD",
      currency: "eur",
      mainAccountId: "",
    });
    expect(Object.keys(errors).sort()).toEqual(["accountCode", "bic", "currency", "iban", "legalEntityId", "mainAccountId", "statementAccount"]);
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
      bankAccountId: "Choose the bank account.",
      file: "Choose the camt.053, BAI2 or MT940 file from the bank.",
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

describe("amounts, rates and percent", () => {
  it("allows only the currency's minor-unit places and positive values", () => {
    expect(validAmount("1250.00", "EUR")).toBe(true);
    expect(validAmount("1250.5", "EUR")).toBe(true);
    expect(validAmount("10.005", "USD")).toBe(false);
    expect(validAmount("10.000", "USD")).toBe(true);
    expect(validAmount("1500", "JPY")).toBe(true);
    expect(validAmount("12.5", "JPY")).toBe(false);
    expect(validAmount("1.234", "KWD")).toBe(true);
    expect(validAmount("0", "EUR")).toBe(false);
    expect(validAmount("0.00", "EUR")).toBe(false);
    expect(validAmount("-5", "EUR")).toBe(false);
  });

  it("moves the decimal point between percent and fraction without float arithmetic", () => {
    expect(fractionFromPercent("3")).toBe("0.03");
    expect(fractionFromPercent("3.125")).toBe("0.03125");
    expect(fractionFromPercent("0.0001")).toBe("0.000001");
    expect(fractionFromPercent("100")).toBe("1");
    expect(fractionFromPercent("12.5")).toBe("0.125");
    expect(percent("0.03")).toBe("3%");
    expect(percent("0.031250")).toBe("3.125%");
    expect(percent("1")).toBe("100%");
    expect(percent("0")).toBe("0%");
  });
});

describe("checks", () => {
  it("validates a draft and offers only the transitions the service allows", () => {
    const draft = { bankAccountId: LE, payee: " Acme ", amount: "1250.00", currency: "USD" };
    expect(validateCheck(draft)).toEqual({});
    expect(toCheckInput(draft)).toEqual({ bankAccountId: LE, payee: "Acme", amount: "1250.00", currency: "USD" });
    expect(Object.keys(validateCheck({ bankAccountId: "x", payee: "", amount: "1.001", currency: "usd" })).sort()).toEqual(["amount", "bankAccountId", "currency", "payee"]);
    expect(CHECK_ACTIONS.created.map((a) => a.action)).toEqual(["print", "void"]);
    expect(CHECK_ACTIONS.printed.map((a) => a.action)).toEqual(["send", "reprint", "void"]);
    expect(CHECK_ACTIONS.sent.map((a) => a.action)).toEqual(["clear", "stop", "reprint", "void"]);
    expect(CHECK_ACTIONS.cleared).toEqual([]);
    expect(CHECK_ACTIONS.voided).toEqual([]);
    expect(CHECK_ACTIONS.printed.some((a) => a.action === "stop")).toBe(false);
  });
});

describe("facilities and instruments", () => {
  const inst: InstrumentDraft = {
    kind: "guarantee",
    reference: " G-1 ",
    beneficiary: "Customs",
    amount: "1000.00",
    issueDate: "2026-11-01",
    expiryDate: "2027-01-31",
    linkedOrderRef: "",
    overrideLimit: false,
  };

  it("checks the facility form", () => {
    expect(validateFacility({ legalEntityId: LE, code: "LC-DB-01", bankName: "Deutsche Bank", currency: "EUR", limit: "1000000.00", expiryDate: "2030-12-31" })).toEqual({});
    expect(Object.keys(validateFacility({ legalEntityId: "", code: "lc 1", bankName: "", currency: "E", limit: "-1", expiryDate: "" })).sort()).toEqual([
      "bankName",
      "code",
      "currency",
      "expiryDate",
      "legalEntityId",
      "limit",
    ]);
  });

  it("sends the facility currency and asks for the override only when chosen", () => {
    expect(validateInstrument(inst, "EUR")).toEqual({});
    expect(toInstrumentInput(inst, "EUR")).toEqual({
      kind: "guarantee",
      reference: "G-1",
      beneficiary: "Customs",
      currency: "EUR",
      amount: "1000.00",
      issueDate: "2026-11-01",
      expiryDate: "2027-01-31",
    });
    expect(toInstrumentInput({ ...inst, overrideLimit: true, linkedOrderRef: "PO-77" }, "EUR")).toMatchObject({ overrideLimit: true, linkedOrderRef: "PO-77" });
    expect(validateInstrument({ ...inst, expiryDate: "2026-10-01" }, "EUR").expiryDate).toMatch(/before the issue date/);
    expect(validateInstrument({ ...inst, amount: "10.5" }, "JPY").amount).toMatch(/JPY/);
  });
});

describe("loans", () => {
  const loan: LoanDraft = {
    legalEntityId: LE,
    role: "lender",
    counterpartyName: "Passage Holdings BV",
    principal: "1000000.00",
    currency: "EUR",
    startDate: "2026-06-01",
    maturityDate: "2026-07-01",
    scheduleType: "Bullet",
    ratePercent: "3.00",
    dayCount: "ACT/360",
    periodMonths: 1,
  };

  it("sends the rate as a fraction", () => {
    expect(validateLoan(loan)).toEqual({});
    expect(toLoanInput(loan)).toMatchObject({ fixedRate: "0.03", principal: "1000000.00", periodMonths: 1 });
  });

  it("refuses a rate above 100%, more than four decimals, and a maturity before the start", () => {
    expect(validateLoan({ ...loan, ratePercent: "100.5" }).ratePercent).toBeDefined();
    expect(validateLoan({ ...loan, ratePercent: "3.12345" }).ratePercent).toBeDefined();
    expect(validateLoan({ ...loan, ratePercent: "100" })).toEqual({});
    expect(validateLoan({ ...loan, maturityDate: "2026-06-01" }).maturityDate).toMatch(/after the start/);
  });
});

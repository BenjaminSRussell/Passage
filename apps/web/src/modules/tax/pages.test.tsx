import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { toEntry, validateMaster } from "./IntrastatMasterPage";
import { toNewLine, validateLine } from "./IntrastatJournalPage";
import { treeRows } from "./JurisdictionsPage";
import { validatePeriod } from "./ReturnPages";
import { rateOn, toInput, validateCode } from "./TaxCodesPage";
import { validateGroup } from "./TaxGroupsPage";
import { validateSetting } from "./TaxHomePage";
import { validateBox, withBox, type BoxDraft } from "./VatBoxesPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const jurisdictions = [
  { code: "US", name: "United States", kind: "country", country: "US", parent_code: "" },
  { code: "US-OH", name: "Ohio", kind: "state", country: "US", parent_code: "US" },
  { code: "DE", name: "Germany", kind: "country", country: "DE", parent_code: "" },
];
const vat = { code: "VAT19", name: "German VAT 19%", jurisdiction: "DE", mechanism: "standard", calc_basis: "line", rounding: "per_line", compound_sequence: 0, recoverable_pct: null, rates: [{ valid_from: "2021-01-01", valid_to: null, rate_pct: "19.0000" }] };

describe("input checks", () => {
  it("refuses a tax code with a bad code, no jurisdiction or a rate over 100", () => {
    const draft = { code: "bad code", name: "", jurisdiction: "", mechanism: "standard" as const, recoverable_pct: "120", rates: [{ valid_from: "", valid_to: "", rate_pct: "150" }] };
    expect(Object.keys(validateCode(draft)).sort()).toEqual(["code", "jurisdiction", "name", "rates.0.rate_pct", "rates.0.valid_from", "recoverable_pct"]);
    const ok = { ...draft, code: "VAT19", name: "VAT", jurisdiction: "DE", recoverable_pct: "", rates: [{ valid_from: "2021-01-01", valid_to: "", rate_pct: "19" }] };
    expect(validateCode(ok)).toEqual({});
    expect(toInput(ok).rates).toEqual([{ valid_from: "2021-01-01", rate_pct: "19" }]);
    expect(validateCode({ ...ok, rates: [{ valid_from: "2021-05-01", valid_to: "2021-01-01", rate_pct: "19" }] })["rates.0.valid_to"]).toBeDefined();
  });

  it("finds the rate in force on a day", () => {
    const code = { ...vat, mechanism: "standard" as const, rates: [{ valid_from: "2020-07-01", valid_to: "2020-12-31", rate_pct: "16" }, { valid_from: "2021-01-01", valid_to: null, rate_pct: "19" }] };
    expect(rateOn(code, "2020-08-01")).toBe("16");
    expect(rateOn(code, "2026-03-01")).toBe("19");
    expect(rateOn(code, "2019-01-01")).toBeUndefined();
  });

  it("orders jurisdictions as a tree and keeps orphans visible", () => {
    const rows = treeRows([...(jurisdictions as never[]), { code: "X", name: "Lost", kind: "city", country: "US", parent_code: "GONE" } as never]);
    expect(rows.map((r) => `${r.depth}:${r.code}`)).toEqual(["0:DE", "0:US", "1:US-OH", "0:X"]);
  });

  it("checks settings, groups, periods and boxes", () => {
    expect(validateSetting({ home_country: "de", vat_id: "" })).toEqual({});
    expect(validateSetting({ home_country: "Germany", vat_id: "x".repeat(21) })).toHaveProperty("home_country");
    expect(validateGroup({ code: "", name: "x", tax_codes: [] })).toHaveProperty("code");
    expect(validatePeriod({ from: "2026-02-01", to: "2026-01-01" })).toHaveProperty("to");
    expect(validatePeriod({ from: "2026-01-01", to: "2026-01-31" })).toEqual({});
    const base: BoxDraft = { box: "81", label: "Sales", kind: "base", sort: "", codes: [] };
    expect(validateBox(base)).toHaveProperty("codes");
    expect(validateBox({ ...base, kind: "payable" })).toEqual({});
    const next = withBox([{ box: "81", label: "old", kind: "base", sort: 10, codes: [] }], { ...base, codes: [{ code: "VAT19", direction: "output" }] });
    expect(next).toHaveLength(1);
    expect(next[0]).toMatchObject({ label: "Sales", codes: [{ code: "VAT19", direction: "output" }] });
  });

  it("checks Intrastat master data and lines", () => {
    const draft = { code: "8413", description: "", supplementary_unit: "", eu_member: true, intrastat_code: "", country: "" };
    expect(Object.keys(validateMaster("commodity", draft))).toEqual(["code", "description"]);
    expect(toEntry("country", { ...draft, code: "GR", description: "Greece", intrastat_code: "el" }).attrs).toEqual({ eu_member: "true", intrastat_code: "EL" });
    const line = { partner_country: "gr", commodity_code: "84137021", transaction_code: "11", transport_mode: "3", delivery_terms: "dap", port: "", origin_country: "", net_mass: "12.5", supplementary_qty: "", invoice_value: "100", statistical_value: "100" };
    expect(validateLine({ ...line, net_mass: "heavy" })).toHaveProperty("net_mass");
    expect(validateLine(line)).toEqual({});
    expect(toNewLine(line, "R1")).toMatchObject({ partner_country: "GR", delivery_terms: "DAP", supplementary_qty: "0", source_ref: "R1" });
  });
});

describe("tax codes page", () => {
  it("lists codes with their jurisdiction name and the rate today, and saves a new one", async () => {
    server.handler = (url, method) => {
      if (url.pathname === "/api/v1/tax/codes" && method === "GET") return json(200, { items: [vat] });
      if (url.pathname === "/api/v1/tax/jurisdictions") return json(200, { items: jurisdictions });
      if (url.pathname === "/api/v1/tax/codes/FR-VAT20" && method === "PUT") return json(200, { ...vat, code: "FR-VAT20" });
      return undefined;
    };
    const { container } = renderAt("/tax/codes");
    const table = await screen.findByRole("table", { name: "Tax codes" });
    expect(await within(table).findByText("Germany")).toBeInTheDocument();
    expect(within(table).getByText("19.0000%")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "FR-VAT20" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "French VAT 20%" } });
    fireEvent.change(screen.getByLabelText("Jurisdiction"), { target: { value: "DE" } });
    fireEvent.change(screen.getByLabelText("Rate 1 starts"), { target: { value: "2014-01-01" } });
    fireEvent.change(screen.getByLabelText("Rate 1 (%)"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Add tax code" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toBe("/api/v1/tax/codes/FR-VAT20");
    expect(put.body).toMatchObject({ name: "French VAT 20%", jurisdiction: "DE", rates: [{ valid_from: "2014-01-01", rate_pct: "20" }] });
  });

  it("does not call the service while the form is invalid", async () => {
    server.handler = (url) => (url.pathname.endsWith("/jurisdictions") || url.pathname.endsWith("/codes") ? json(200, { items: [] }) : undefined);
    renderAt("/tax/codes");
    fireEvent.click(await screen.findByRole("button", { name: "Add tax code" }));
    expect(await screen.findByText("Choose the jurisdiction this tax belongs to.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("explains a code that is still in use in plain words", async () => {
    server.handler = (url, method) => {
      if (url.pathname === "/api/v1/tax/codes" && method === "GET") return json(200, { items: [vat] });
      if (url.pathname === "/api/v1/tax/jurisdictions") return json(200, { items: jurisdictions });
      if (method === "DELETE") return json(422, { code: "TAX-0013", serviceCode: "TAX-0013", detail: "Tax code VAT19 is used by 1 tax groups and 0 VAT return boxes." });
      return undefined;
    };
    renderAt("/tax/codes");
    fireEvent.click(await screen.findByRole("button", { name: "Delete German VAT 19%" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete tax code" }));
    expect(await screen.findByText(/This tax setup item is still used by something else/)).toBeInTheDocument();
  });
});

describe("VAT return and EU sales list pages", () => {
  it("shows the box totals and warns about transactions without a taxable amount", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/tax/vat-return"
        ? json(200, { period_from: "2026-10-01", period_to: "2026-10-31", boxes: [{ box: "81", label: "Supplies taxed at 19%", kind: "base", amount: "1500.00" }], transactions: 3, missing_base: 1, unmapped: 0 })
        : undefined;
    renderAt("/tax/vat-return");
    const table = await screen.findByRole("table", { name: "VAT return boxes" });
    expect(await within(table).findByText("1500.00")).toBeInTheDocument();
    expect(screen.getByText(/1 recorded transaction has no taxable amount/)).toBeInTheDocument();
  });

  it("asks for the company country when the sales list has no settings", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tax/esl" ? json(422, { code: "COM-0503", serviceCode: "COM-0503", detail: "no settings" }) : undefined);
    renderAt("/tax/esl");
    expect(await screen.findByText("Set the country of the VAT return first")).toBeInTheDocument();
  });
});

describe("Intrastat journal page", () => {
  const line = (n: number, errors: string[] = []) => ({
    line_no: n, source_ref: `S${n}`, partner_country: "GR", commodity_code: "84137021", transaction_code: "11", transport_mode: "3", delivery_terms: "DAP", port: "", origin_country: "US",
    net_mass: "10.000", supplementary_qty: "1.000", invoice_value: "500.00", statistical_value: "500.00", compressed_from: 1, errors,
  });
  const journal = (status: string, lines = [line(1, ["Commodity code 99999999 is not in the commodity master data."])]) => ({
    id: "j1", flow: "dispatch", period_from: "2026-10-01", status, correction_of: "", created_at: "2026-10-02T00:00:00Z", exported_at: null, line_count: lines.length, error_count: lines.filter((l) => l.errors.length > 0).length, lines,
  });

  it("shows the problems of a line by country name and blocks the export until the lines are checked", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tax/intrastat/journals/j1" ? json(200, journal("draft")) : undefined);
    const { container } = renderAt("/tax/intrastat/journals/j1");
    expect(await screen.findByText("Greece (GR)")).toBeInTheDocument();
    expect(screen.getByText("Commodity code 99999999 is not in the commodity master data.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Dispatches, October 2026/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export declaration" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("checks the lines and merges matching ones through the service", async () => {
    server.handler = (url, method) => {
      if (url.pathname === "/api/v1/tax/intrastat/journals/j1" && method === "GET") return json(200, journal("draft", [line(1), line(2)]));
      if (url.pathname.endsWith("/validate")) return json(200, journal("validated", [line(1), line(2)]));
      if (url.pathname.endsWith("/compress")) return json(200, journal("draft", [{ ...line(1), compressed_from: 2 }]));
      return undefined;
    };
    renderAt("/tax/intrastat/journals/j1");
    fireEvent.click(await screen.findByRole("button", { name: "Merge matching lines" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith("/compress") && c.method === "POST")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Check lines" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith("/validate"))).toBe(true));
  });

  it("offers a correction, not edits, once the journal is exported", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tax/intrastat/journals/j1" ? json(200, journal("exported", [line(1)])) : undefined);
    renderAt("/tax/intrastat/journals/j1");
    expect(await screen.findByRole("button", { name: "Start a correction" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add line" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Remove line/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download again" })).toBeEnabled();
  });
});

describe("company tax settings", () => {
  it("asks for the country when none is saved and saves it", async () => {
    server.handler = (url, method) => {
      if (url.pathname === "/api/v1/tax/entity-settings" && method === "GET") return json(422, { code: "COM-0503", serviceCode: "COM-0503", detail: "none" });
      if (url.pathname === "/api/v1/tax/entity-settings" && method === "PUT") return json(200, { home_country: "DE", vat_id: "DE812345678" });
      if (url.pathname.startsWith("/api/v1/tax/")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/tax");
    expect(await screen.findByText("Tax settings are not set yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Country of the VAT return"), { target: { value: "de" } });
    fireEvent.change(screen.getByLabelText(/Company VAT number/), { target: { value: "DE812345678" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tax settings" }));
    await waitFor(() => expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ home_country: "DE", vat_id: "DE812345678" }));
  });
});

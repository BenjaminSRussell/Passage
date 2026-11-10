import { describe, expect, it, vi } from "vitest";
import {
  createJournal,
  deleteTaxCode,
  exportJournal,
  getEntitySetting,
  getVatReturn,
  listJurisdictions,
  putMaster,
  putTaxCode,
  putTaxGroup,
  TaxApiError,
  transferLines,
} from "./client";

const ok = (body: unknown, status = 200) => vi.fn(async () => new Response(JSON.stringify(body), { status }));

describe("tax client", () => {
  it("reads the items of a list and uses the tax base path", async () => {
    const f = ok({ items: [{ code: "US", name: "United States", kind: "country", country: "US", parent_code: "" }] });
    const rows = await listJurisdictions(f);
    expect(rows[0].name).toBe("United States");
    expect(f).toHaveBeenCalledWith("/api/v1/tax/jurisdictions", expect.anything());
  });

  it("puts a tax code at its encoded path with the rates in the body", async () => {
    const f = ok({ code: "A/B", name: "x", jurisdiction: "DE", mechanism: "standard", calc_basis: "line", rounding: "per_line", compound_sequence: 0, rates: [] });
    await putTaxCode("A/B", { name: "x", jurisdiction: "DE", mechanism: "standard", rates: [{ valid_from: "2026-01-01", rate_pct: "19" }] }, f);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/tax/codes/A%2FB");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(String(init.body)).rates).toEqual([{ valid_from: "2026-01-01", rate_pct: "19" }]);
  });

  it("sends a group without its kind and code in the body, which the path carries", async () => {
    const f = ok({ kind: "item", code: "G", name: "Goods", tax_codes: ["VAT"] });
    await putTaxGroup({ kind: "item", code: "G", name: "Goods", tax_codes: ["VAT"] }, f);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/tax/groups/item/G");
    expect(JSON.parse(String(init.body))).toEqual({ name: "Goods", tax_codes: ["VAT"] });
  });

  it("puts master data with its attributes", async () => {
    const f = ok({ kind: "commodity", code: "84137021", description: "Pumps", attrs: { supplementary_unit: "p/st" } });
    await putMaster({ kind: "commodity", code: "84137021", description: "Pumps", attrs: { supplementary_unit: "p/st" } }, f);
    expect((f.mock.calls as unknown as string[][])[0][0]).toBe("/api/v1/tax/intrastat/master/commodity/84137021");
  });

  it("passes the period as query parameters", async () => {
    const f = ok({ period_from: "2026-01-01", period_to: "2026-01-31", boxes: [], transactions: 0, missing_base: 0, unmapped: 0 });
    await getVatReturn("2026-01-01", "2026-01-31", f);
    expect(f).toHaveBeenCalledWith("/api/v1/tax/vat-return?period_from=2026-01-01&period_to=2026-01-31", expect.anything());
  });

  it("treats the service's not-configured answer for entity settings as no settings yet", async () => {
    expect(await getEntitySetting(ok({ serviceCode: "COM-0503", detail: "no settings" }, 422))).toBeNull();
    await expect(getEntitySetting(ok({ code: "COM-0500" }, 500))).rejects.toBeInstanceOf(TaxApiError);
  });

  it("raises the service code and field errors of a problem", async () => {
    const f = ok({ code: "validation_failed", serviceCode: "COM-0001", detail: "bad", errors: { name: "required" } }, 400);
    const error = await deleteTaxCode("X", f).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TaxApiError);
    expect(error).toMatchObject({ status: 400, code: "COM-0001", fields: { name: "required" } });
  });

  it("opens and transfers into journals and reads the export as text", async () => {
    const journal = { id: "j", flow: "dispatch", period_from: "2026-10-01", status: "draft", correction_of: "", created_at: "2026-10-02T00:00:00Z", exported_at: null, line_count: 0, error_count: 0 };
    const created = ok(journal, 201);
    await createJournal("dispatch", "2026-10-01", created);
    expect(JSON.parse(String((created.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({ flow: "dispatch", period_from: "2026-10-01" });
    const moved = ok({ added: 1, skipped: 0 });
    expect(await transferLines("j", [], moved)).toEqual({ added: 1, skipped: 0 });
    const csv = vi.fn(async () => new Response("line,flow\n1,dispatch\n", { status: 200 }));
    expect(await exportJournal("j", csv)).toContain("1,dispatch");
    expect((csv.mock.calls as unknown as string[][])[0][0]).toBe("/api/v1/tax/intrastat/journals/j/export");
  });
});

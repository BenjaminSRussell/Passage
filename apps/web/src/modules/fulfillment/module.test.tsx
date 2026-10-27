import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { compareDecimal, fixed, multiplyDecimal, subtractDecimal, sumDecimals } from "./format";
import fulfillment from "./module";
import { ScanPage } from "./ScanPage";

describe("fulfillment module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /fulfillment", () => {
    expect(fulfillment.id).toBe("fulfillment");
    expect(fulfillment.routes.every((r) => r.element != null && r.path?.startsWith("/fulfillment"))).toBe(true);
    expect(fulfillment.nav.every((n) => n.to.startsWith("/fulfillment") && n.duty)).toBe(true);
    const paths = fulfillment.routes.map((r) => r.path);
    for (const nav of fulfillment.nav) expect(paths).toContain(nav.to);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("fulfillment");
  });
});

describe("scan page (FR-28.11, AC-28.9)", () => {
  it("parses a GS1-128 scan into GTIN, batch and quantity", () => {
    render(<ScanPage />);
    const field = screen.getByLabelText("GS1-128 scan");
    expect(field).toHaveFocus();
    fireEvent.change(field, { target: { value: "(01)00614141000012(10)LOT7(37)5" } });
    fireEvent.click(screen.getByRole("button", { name: "Parse scan" }));
    const fields = screen.getByLabelText("Scanned fields");
    expect(fields.textContent).toContain("GTIN00614141000012");
    expect(fields.textContent).toContain("BatchLOT7");
    expect(fields.textContent).toContain("Quantity5");
    expect(fields.textContent).not.toContain("Serial");
  });

  it("shows FUL-0017 for a scan whose check digit is wrong", () => {
    render(<ScanPage />);
    fireEvent.change(screen.getByLabelText("GS1-128 scan"), { target: { value: "(01)00614141000013" } });
    fireEvent.click(screen.getByRole("button", { name: "Parse scan" }));
    expect(screen.getByRole("alert").textContent).toContain("FUL-0017");
    expect(screen.queryByLabelText("Scanned fields")).toBeNull();
  });
});

describe("decimal helpers (no floats on quantities or money)", () => {
  it("multiplies and rounds half away from zero like the service", () => {
    expect(multiplyDecimal("10", "12.50", 2)).toBe("125.00");
    expect(multiplyDecimal("3", "2.005", 2)).toBe("6.02");
    expect(multiplyDecimal("0.1", "0.2", 2)).toBe("0.02");
  });

  it("subtracts, compares and sums exactly", () => {
    expect(subtractDecimal("100", "90")).toBe("10");
    expect(subtractDecimal("0.3", "0.1")).toBe("0.2");
    expect(subtractDecimal("1", "1.000")).toBe("0");
    expect(compareDecimal("90.5", "90.50")).toBe(0);
    expect(compareDecimal("101", "100")).toBe(1);
    expect(sumDecimals(["0.10", "0.20"], 2)).toBe("0.30");
    expect(fixed("-125", 2)).toBe("-125.00");
  });
});

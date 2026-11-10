import { render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Balance, Rule } from "./api/client";
import { controlLevel, controlRows, controlTotals, percentOf, periodRows } from "./costControl";
import { CostControlPage } from "./CostControlPage";

const LE = "11111111-1111-4111-8111-111111111111";
const RULE = "22222222-2222-4222-8222-222222222222";

function bal(dim: string, span: string, budget: string, actuals: string, enc: string, pre = "0", res = "0"): Balance {
  return { id: dim + span, ruleVersionId: RULE, controlDimensionKey: dim, timeSpanKey: span, budgetTotal: budget, actuals, encumbrances: enc, preEncumbrances: pre, reservations: res, available: "0", version: 1 };
}

const balances = [
  bal("CC1", "2026-Q3", "1000", "400", "100"),
  bal("CC1", "2026-Q4", "1000", "950", "0"),
  bal("CC2", "2026-Q4", "500", "300", "150", "100", "10"),
  bal("CC3", "2026-Q4", "0", "0", "0"),
];

describe("cost control maths", () => {
  it("rounds percentages half away from zero and has none without a plan", () => {
    expect(percentOf(1n, 3n)).toBe("33.33");
    expect(percentOf(2n, 3n)).toBe("66.67");
    expect(percentOf(-1n, 3n)).toBe("-33.33");
    expect(percentOf(5n, 0n)).toBeNull();
  });

  it("calls a combination near from 90 percent used and over once it exceeds the plan", () => {
    expect(controlLevel(1000n, 899n)).toBe("ok");
    expect(controlLevel(1000n, 900n)).toBe("near");
    expect(controlLevel(1000n, 1000n)).toBe("near");
    expect(controlLevel(1000n, 1001n)).toBe("over");
    expect(controlLevel(0n, 1n)).toBe("over");
    expect(controlLevel(0n, 0n)).toBe("ok");
  });

  it("counts pre-encumbrances as committed and earmarks as used, and leaves plan minus used", () => {
    const rows = controlRows(balances);
    const cc2 = rows.find((r) => r.dimension === "CC2")!;
    expect(cc2.committed).toBe("250.00");
    expect(cc2.earmarked).toBe("10.00");
    expect(cc2.variance).toBe("-60.00");
    expect(cc2.usedPct).toBe("112.00");
    expect(cc2.level).toBe("over");
    expect(rows.find((r) => r.dimension === "CC3")!.usedPct).toBeNull();
  });

  it("totals across combinations and compares periods", () => {
    const totals = controlTotals(controlRows(balances));
    expect(totals).toMatchObject({ plan: "2500.00", actual: "1650.00", committed: "350.00", variance: "490.00", over: 1, near: 1 });
    const periods = periodRows(balances);
    expect(periods.map((p) => p.span)).toEqual(["2026-Q3", "2026-Q4"]);
    expect(periods[0].actualChangePct).toBeNull();
    expect(periods[1].actual).toBe("1250.00");
    expect(periods[1].actualChangePct).toBe("212.50");
  });
});

const rule: Rule = { id: RULE, legalEntityId: LE, code: "DEPT", version: 1, warnPct: "0", blockPct: "5", currency: "USD", effectiveFrom: "2026-01-01" };

afterEach(() => vi.unstubAllGlobals());

describe("cost control page", () => {
  it("shows the summary, flags combinations over the plan and compares periods", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/control-rules")) return new Response(JSON.stringify({ items: [rule] }), { status: 200 });
        if (url.includes("/available")) return new Response(JSON.stringify({ items: balances }), { status: 200 });
        return new Response("{}", { status: 404 });
      }),
    );
    const { container } = render(
      createElement(MemoryRouter, { initialEntries: [`/budgeting/cost-control?legalEntityId=${LE}&rule=${RULE}`] }, createElement(Routes, null, createElement(Route, { path: "/budgeting/cost-control", element: createElement(CostControlPage) }))),
    );
    await screen.findByRole("table", { name: "Cost control by combination" });
    await waitFor(() => expect(screen.getAllByText("Over the plan").length).toBeGreaterThan(0));
    expect(screen.getByText("Near the plan")).toBeTruthy();
    expect(screen.getByRole("table", { name: "Cost control by period" })).toBeTruthy();
    expect(screen.getByText("212.50%")).toBeTruthy();
    const shell = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
    expect(checkAxeAA(container).filter((v) => !shell.has(v.id))).toEqual([]);
  });
});

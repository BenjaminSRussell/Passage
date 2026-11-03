import { describe, expect, it } from "vitest";
import { emptyLine, validateRelease } from "./ReleasePage";
import { SKIP_LABELS } from "./format";

const ID = "11111111-1111-4111-8111-111111111111";
const base = { ...emptyLine(), orderId: ID, orderLineId: ID, customerId: ID, legalEntityId: ID, productId: ID, qty: "10" };
const values = (line: typeof base) => ({ warehouseId: ID, horizonDays: "", requireFullAtp: false, lines: [line] });

describe("release line order type, priority and ATP quantity", () => {
  it("sends nothing extra for a plain order", () => {
    const { input } = validateRelease(values(base));
    expect(input?.lines[0]).not.toHaveProperty("orderType");
    expect(input?.lines[0]).not.toHaveProperty("priority");
    expect(input?.lines[0]).not.toHaveProperty("atpQty");
  });

  it("sends a journal order type, a priority and an ATP quantity", () => {
    const { input, errors } = validateRelease(values({ ...base, orderType: "JOURNAL", priority: "2", atpQty: "4.5" }));
    expect(errors).toEqual({});
    expect(input?.lines[0]).toMatchObject({ orderType: "JOURNAL", priority: 2, atpQty: "4.5" });
  });

  it("refuses a priority outside 1 to 9 and a bad ATP quantity", () => {
    const { errors } = validateRelease(values({ ...base, priority: "10", atpQty: "-1" }));
    expect(errors["lines[0].priority"]).toBeTruthy();
    expect(errors["lines[0].atpQty"]).toBeTruthy();
  });

  it("explains a journal order skip", () => {
    expect(SKIP_LABELS.journal_order).toMatch(/never ships/);
  });

  it("explains a line sales cut back that is already released", () => {
    expect(SKIP_LABELS.order_remainder_cancelled).toMatch(/cancelled the remainder/);
  });
});

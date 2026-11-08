import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { CreateProductionOrderPage, initialValues, toInput, validateOrder } from "./CreateOrderPage";

const uuid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;

const filled = () => {
  const v = initialValues();
  v.productId = uuid(1);
  v.siteId = uuid(2);
  v.warehouseId = uuid(3);
  v.quantity = "100";
  v.lines = [{ lineNo: "10", itemId: uuid(4), qtyPer: "3", scrapPct: "5", unitCost: "10" }];
  v.operations = [{ operNumber: "10", resourceId: uuid(5), setupTimeH: "", runTimeHPer: "0.5", laborRate: "30", overheadRate: "" }];
  return v;
};

describe("Released product order rules (FR-21.1)", () => {
  it("sends the released product id and leaves it out when empty", () => {
    expect(toInput(filled()).releasedProductId).toBeUndefined();
    expect(toInput({ ...filled(), releasedProductId: ` ${uuid(9)} ` }).releasedProductId).toBe(uuid(9));
  });

  it("accepts an empty quantity only with a released product, and checks the id", () => {
    expect(validateOrder({ ...filled(), quantity: "" })).toHaveProperty("quantity");
    const v = { ...filled(), quantity: "", releasedProductId: uuid(9) };
    expect(validateOrder(v)).toEqual({});
    expect(toInput(v).quantity).toBeUndefined();
    expect(validateOrder({ ...filled(), releasedProductId: "FG-100" })).toHaveProperty("releasedProductId");
    expect(validateOrder({ ...filled(), quantity: "0", releasedProductId: uuid(9) })).toHaveProperty("quantity");
  });

  it("shows the field on the form and flags a malformed id", async () => {
    render(<CreateProductionOrderPage />);
    await userEvent.type(screen.getByLabelText(/released product/i), "FG-100");
    await userEvent.click(screen.getByRole("button", { name: /^create/i }));
    expect(await screen.findByText(/released product's id/i)).toBeInTheDocument();
  });
});

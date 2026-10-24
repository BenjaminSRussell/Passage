import { render, screen } from "@testing-library/react";
import { loadModules } from "../registry";
import product from "./module";
import { CatalogPage, ProductHome } from "./pages";

describe("product module", () => {
  it("uses its folder name as id and declares product routes", () => {
    expect(product.id).toBe("product");
    expect(product.routes.length).toBeGreaterThan(0);
    expect(product.nav.every((n) => n.to.startsWith("/product"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("product");
  });

  it("shows PIM workspace tiles for the demo catalog", () => {
    render(<ProductHome />);
    expect(screen.getByRole("heading", { name: "Product information management" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "PIM tiles" })).toHaveTextContent("Masters 50");
    expect(screen.getByRole("list", { name: "PIM tiles" })).toHaveTextContent("Products 500");
  });

  it("lists products and masters", () => {
    render(<CatalogPage />);
    expect(screen.getByRole("table", { name: "Products" })).toHaveTextContent("SHIRT");
  });
});

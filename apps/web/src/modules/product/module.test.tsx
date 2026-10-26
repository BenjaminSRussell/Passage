import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { loadModules } from "../registry";
import product from "./module";
import { CatalogPage, ProductDetail, ProductHome } from "./pages";

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
    expect(screen.getByRole("heading", { name: "Product information" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "PIM tiles" })).toHaveTextContent("Masters 50");
    expect(screen.getByRole("list", { name: "PIM tiles" })).toHaveTextContent("Products 500");
  });

  it("lists products and masters", () => {
    render(<CatalogPage />);
    expect(screen.getByRole("table", { name: "Products" })).toHaveTextContent("SHIRT");
  });

  it("labels subtype and state in plain words", () => {
    render(<CatalogPage />);
    const table = screen.getByRole("table", { name: "Products" });
    expect(table).toHaveTextContent("Master");
    expect(table).toHaveTextContent("Active");
  });

  it("opens a product from its number", () => {
    const opened: string[] = [];
    render(<CatalogPage onOpen={(number) => opened.push(number)} />);
    fireEvent.click(screen.getByRole("button", { name: "BOLT" }));
    expect(opened).toEqual(["BOLT"]);
  });

  it("explains an empty catalog", () => {
    render(<CatalogPage items={[]} />);
    expect(screen.getByText("No products yet")).toBeInTheDocument();
  });

  it("marks the demo catalog as sample data", () => {
    render(<CatalogPage />);
    expect(screen.getByText("Sample data")).toBeInTheDocument();
  });

  it("shows the product named in the address, and a not-found state for an unknown one", () => {
    function renderAt(path: string) {
      return render(
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            {product.routes.map((route) => (
              <Route key={route.path} path={route.path} element={route.element} />
            ))}
          </Routes>
        </MemoryRouter>,
      );
    }
    const known = renderAt("/product/BOLT");
    expect(screen.getByRole("heading", { name: "Hex bolt" })).toBeInTheDocument();
    known.unmount();
    renderAt("/product/NOPE");
    expect(screen.getByRole("heading", { name: "Product not found" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to products" })).toBeInTheDocument();
  });

  it("shows product identifiers in a definition list", () => {
    const { container } = render(<ProductDetail />);
    expect(screen.getByRole("heading", { name: "Knit shirt" })).toBeInTheDocument();
    expect(container.querySelector("dl")).toHaveTextContent("06141411234579");
  });
});

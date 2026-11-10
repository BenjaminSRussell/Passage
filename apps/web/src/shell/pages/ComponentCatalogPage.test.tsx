import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { checkAxeAA } from "../a11y";
import { ComponentCatalogPage, catalogEntries } from "./ComponentCatalogPage";

describe("component catalog", () => {
  it("renders every entry with its rule and has no axe violations", () => {
    const view = render(
      <MemoryRouter>
        <ComponentCatalogPage />
      </MemoryRouter>,
    );
    for (const entry of catalogEntries) {
      expect(screen.getByRole("heading", { level: 2, name: entry.name })).toBeInTheDocument();
    }
    expect(screen.getByRole("heading", { level: 2, name: "Status standard" })).toBeInTheDocument();
    expect(checkAxeAA(view.container).filter((v) => !["region", "bypass", "page-has-heading-one", "landmark-unique"].includes(v.id))).toEqual([]);
  });

  it("covers the components the design system names", () => {
    const names = catalogEntries.map((e) => e.name.toLowerCase());
    for (const required of ["page header", "form field", "table", "status badge", "dialog", "alert", "empty state", "loading skeleton"]) {
      expect(names.some((n) => n.startsWith(required)), required).toBe(true);
    }
  });
});

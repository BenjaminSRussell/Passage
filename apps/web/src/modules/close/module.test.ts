import { render, screen } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import close from "./module";

describe("close module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(close.id).toBe("close");
    expect(close.routes.length).toBeGreaterThan(0);
    expect(close.nav.every((n) => n.to.startsWith("/close"))).toBe(true);
  });

  it("renders a page for its route", () => {
    render(createElement(MemoryRouter, null, close.routes[0].element));
    expect(screen.getByRole("heading", { name: "This workspace isn't available yet" })).toBeInTheDocument();
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("close");
  });
});

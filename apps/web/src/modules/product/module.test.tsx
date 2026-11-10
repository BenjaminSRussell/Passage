import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { loadModules } from "../registry";
import product from "./module";
import { validate } from "./ProductForms";

interface Call {
  method: string;
  url: URL;
  body: unknown;
  headers: Headers;
}
type Reply = { status: number; body: unknown };
const json = (status: number, body: unknown): Reply => ({ status, body });

let handler: (call: Call) => Reply | undefined = () => undefined;
let calls: Call[] = [];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const call: Call = {
        method: init.method ?? "GET",
        url: new URL(input, "http://localhost"),
        body: init.body ? JSON.parse(String(init.body)) : undefined,
        headers: new Headers(init.headers),
      };
      calls.push(call);
      const reply = handler(call) ?? json(404, { code: "COM-0001", detail: "product not found" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const row = (extra: Record<string, unknown> = {}) => ({
  ID,
  ProductNumber: "BOLT",
  Subtype: "product",
  ProductType: "item",
  Name: "Hex bolt",
  Description: "M8 steel",
  BaseUOM: "EA",
  LifecycleState: "draft",
  Version: 1,
  ...extra,
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname}</p>;
}

function renderAt(path: string, duties: string[] = ["product.read", "product.write"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {product.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
        <Where />
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));
const type = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe("product module", () => {
  it("uses its folder name as id and declares product routes", () => {
    expect(product.id).toBe("product");
    expect(product.nav.every((n) => n.to.startsWith("/product"))).toBe(true);
    expect(loadModules().map((m) => m.id)).toContain("product");
  });

  it("shows no sample data anywhere", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/product");
    expect(screen.queryByText("Sample data")).toBeNull();
    expect(screen.queryByText("SHIRT")).toBeNull();
  });
});

describe("product list", () => {
  it("lists products from the service, whatever the casing of its JSON", async () => {
    handler = () => json(200, { items: [row(), { id: "p-2", productNumber: "SHIRT", name: "Knit shirt", subtype: "master", baseUom: "EA", lifecycleState: "active", version: 2 }] });
    const { container } = renderAt("/product/items");
    const table = await screen.findByRole("table", { name: "Products" });
    await waitFor(() => expect(table).toHaveTextContent("Hex bolt"));
    expect(table).toHaveTextContent("Master");
    expect(table).toHaveTextContent("Active");
    expect(calls[0]?.url.pathname).toBe("/api/v1/product/products");
    expect(violations(container)).toEqual([]);
  });

  it("searches by number or name and says plainly when nothing matches", async () => {
    handler = (call) => json(200, { items: call.url.searchParams.get("q") === "zzz" ? [] : [row()] });
    renderAt("/product/items");
    await screen.findByText("Hex bolt");
    type("Search by number or name", " zzz ");
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("No products match your search")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("q")).toBe("zzz");
  });

  it("pages by cursor and appends the next page", async () => {
    handler = (call) =>
      call.url.searchParams.get("cursor") === "c1"
        ? json(200, { items: [row({ ID: "p-3", ProductNumber: "NUT", Name: "Hex nut" })] })
        : json(200, { items: [row()], nextCursor: "c1" });
    renderAt("/product/items");
    fireEvent.click(await screen.findByRole("button", { name: "Show more products" }));
    await screen.findByText("Hex nut");
    expect(screen.getByText("Hex bolt")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show more products" })).toBeNull();
  });

  it("offers a single retry when loading more fails", async () => {
    handler = (call) => (call.url.searchParams.get("cursor") ? json(503, { code: "COM-0503", detail: "down" }) : json(200, { items: [row()], nextCursor: "c1" }));
    renderAt("/product/items");
    fireEvent.click(await screen.findByRole("button", { name: "Show more products" }));
    expect(await screen.findByText("Could not load more products")).toBeInTheDocument();
    expect(screen.getByText("Hex bolt")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Try again|Show more products/ })).toHaveLength(1);
  });

  it("explains an empty catalog and offers to add a product", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/product/items");
    expect(await screen.findByText("No products yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New product" })).toBeInTheDocument();
  });

  it("shows a plain error with a retry when the list can't load", async () => {
    handler = () => json(503, { code: "COM-0503", detail: "down" });
    renderAt("/product/items", ["product.read"]);
    expect(await screen.findByText("Could not load the products")).toBeInTheDocument();
    expect(screen.getByText(/unavailable right now/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New product" })).toBeNull();
    handler = () => json(200, { items: [row()] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Hex bolt")).toBeInTheDocument();
  });

  it("opens a product from its row", async () => {
    handler = () => json(200, { items: [row()] });
    renderAt("/product/items");
    fireEvent.click(await screen.findByRole("button", { name: "BOLT" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/product/${ID}`);
  });
});

describe("product detail", () => {
  it("shows the product from the service", async () => {
    handler = () => json(200, row());
    const { container } = renderAt(`/product/${ID}`);
    expect(await screen.findByRole("heading", { name: "Hex bolt" })).toBeInTheDocument();
    expect(container.querySelector("dl")).toHaveTextContent("M8 steel");
    expect(container.querySelector("dl")).toHaveTextContent("EA");
    expect(screen.getByRole("button", { name: "Edit product" })).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("treats an old number-style address as not found without calling the service", async () => {
    renderAt("/product/BOLT");
    expect(await screen.findByRole("heading", { name: "Product not found" })).toBeInTheDocument();
    expect(calls).toEqual([]);
  });

  it("shows a not-found state for an unknown product", async () => {
    renderAt(`/product/${ID}`);
    expect(await screen.findByRole("heading", { name: "Product not found" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to products" })).toBeInTheDocument();
  });

  it("hides editing from someone who can only read", async () => {
    handler = () => json(200, row());
    renderAt(`/product/${ID}`, ["product.read"]);
    await screen.findByRole("heading", { name: "Hex bolt" });
    expect(screen.queryByRole("button", { name: "Edit product" })).toBeNull();
  });
});

describe("create product", () => {
  it("validates before sending anything", () => {
    const errors = validate({ productNumber: "bad number", name: " ", description: "", baseUom: "", subtype: "product", productType: "item", lifecycleState: "draft" }, true);
    expect(Object.keys(errors).sort()).toEqual(["baseUom", "name", "productNumber"]);
  });

  it("creates with an Idempotency-Key, upper-cased number and unit, then opens it", async () => {
    handler = (call) => (call.method === "POST" ? json(201, row()) : json(200, row()));
    const { container } = renderAt("/product/items/new");
    expect(violations(container)).toEqual([]);
    type("Product number", "bolt");
    type("Name", " Hex bolt ");
    type("Base unit", "ea");
    fireEvent.click(screen.getByRole("button", { name: "Create product" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/product/${ID}`));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/product/products");
    expect(post.body).toEqual({ productNumber: "BOLT", name: "Hex bolt", baseUom: "EA", subtype: "product", productType: "item" });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("marks the fields that need fixing and sends nothing", () => {
    renderAt("/product/items/new");
    fireEvent.click(screen.getByRole("button", { name: "Create product" }));
    expect(screen.getByText("Enter a product number.")).toBeInTheDocument();
    expect(screen.getByText("Enter a name for the product.")).toBeInTheDocument();
    expect(screen.getByLabelText("Product number")).toHaveAttribute("aria-invalid", "true");
    expect(calls).toEqual([]);
  });

  it("tells the person what to do when the number is already used", async () => {
    handler = () => json(409, { code: "PRD-NUM-FORMAT", detail: "product number already exists", errors: { existingId: ID } });
    renderAt("/product/items/new");
    type("Product number", "BOLT");
    type("Name", "Hex bolt");
    type("Base unit", "EA");
    fireEvent.click(screen.getByRole("button", { name: "Create product" }));
    await screen.findByText("Could not create the product");
    const hint = screen.getByLabelText("Product number").getAttribute("aria-describedby") ?? "";
    expect(hint).toContain("product-number-error");
    expect(document.getElementById("product-number-error")).toHaveTextContent("Enter an unused number in the format your company uses.");
    expect(screen.getByTestId("where")).toHaveTextContent("/product/items/new");
  });

  it("needs the write duty", () => {
    renderAt("/product/items/new", ["product.read"]);
    expect(screen.getByText("You can't change products")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create product" })).toBeNull();
  });
});

describe("edit product", () => {
  it("saves under the version it read and returns to the product", async () => {
    handler = (call) => (call.method === "PATCH" ? json(200, row({ Name: "Hex bolt M8", Version: 2 })) : json(200, row()));
    const { container } = renderAt(`/product/${ID}/edit`);
    await screen.findByLabelText("Name");
    expect(violations(container)).toEqual([]);
    expect(screen.getByLabelText("Product number")).toHaveAttribute("readonly");
    type("Name", "Hex bolt M8");
    type("State", "active");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/product/${ID}`));
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.headers.get("If-Match")).toBe('"1"');
    expect(patch.body).toEqual({ name: "Hex bolt M8", description: "M8 steel", lifecycleState: "active" });
  });

  it("explains a conflicting save in plain words", async () => {
    handler = (call) => (call.method === "PATCH" ? json(412, { code: "PRD-VERSION-CONFLICT", detail: "changed" }) : json(200, row()));
    renderAt(`/product/${ID}/edit`);
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(/Someone else changed this product after you opened it/)).toBeInTheDocument();
    handler = () => json(200, row({ Name: "Hex bolt v2", Version: 2 }));
    fireEvent.click(within(alert).getByRole("button", { name: /Reload product/ }));
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Hex bolt v2"));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows server field errors next to the field", async () => {
    handler = (call) => (call.method === "PATCH" ? json(400, { code: "COM-0001", detail: "bad", errors: { name: "must not be empty" } }) : json(200, row()));
    renderAt(`/product/${ID}/edit`);
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must not be empty")).toBeInTheDocument();
  });
});

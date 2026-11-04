// Returnable packaging movements and balance corrections (FR-28.16): list on open, filter,
// empty and error states, and the correction form for a caller holding ReturnablePackagingMaintain.
import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ContainerType, ReturnableMovement } from "./api/client";
import { MOVEMENT_LIMIT } from "./ReturnableMovementsPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const MOVEMENTS = "/api/v1/fulfillment/returnable-packaging/movements";
const ADJUST = "/api/v1/fulfillment/returnable-packaging:adjust";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000030";
const SLIP = "0195f0a0-0000-7000-8000-000000000005";
const pallet: ContainerType = { code: "PALLET", tareKg: "25", returnable: true, packagingMaterial: "WOOD" };
const carton: ContainerType = { code: "C12", tareKg: "0.5", returnable: false, packagingMaterial: "CARDBOARD" };

function movement(id: string, patch: Partial<ReturnableMovement> = {}): ReturnableMovement {
  return {
    id,
    customerId: CUSTOMER,
    containerType: "PALLET",
    kind: "SHIPPED",
    qty: "10",
    balanceAfter: "10",
    reference: SLIP,
    reason: null,
    actor: null,
    createdAt: "2026-10-26T08:00:00Z",
    ...patch,
  };
}

const calls = (path: string, method = "GET") => server.calls.filter((c) => c.method === method && c.url.pathname === path);
const maintain = ["WarehouseView", "ReturnablePackagingMaintain"];

describe("Returnable packaging movements", () => {
  it("lists the newest movements with signed quantities and links a slip", async () => {
    server.handler = (url) =>
      url.pathname === MOVEMENTS
        ? json(200, {
            items: [
              movement("0195f0a0-0000-7000-8000-0000000000a2", { kind: "ADJUSTED", qty: "-3", balanceAfter: "7", reference: "sheet 9", reason: "Lost in transit", actor: "0195f0a0-0000-7000-8000-0000000000c1" }),
              movement("0195f0a0-0000-7000-8000-0000000000a1"),
            ],
          })
        : undefined;
    const { container } = renderAt("/fulfillment/returnable-movements");
    expect(screen.getByRole("table", { name: "Returnable packaging movements" })).toHaveAttribute("aria-busy", "true");

    const table = await findTable({ name: "Returnable packaging movements" });
    const rows = within(table).getAllByRole("row").slice(1);
    const cells = rows.map((row) => Array.from(row.querySelectorAll("td, th")).map((cell) => cell.textContent));
    expect(cells[0]).toEqual(["2026-10-26 08:00 UTC", "0195f0a0", "PALLET", "Corrected", "-3", "7", "sheet 9", "Lost in transit", "0195f0a0"]);
    expect(cells[1].slice(3, 6)).toEqual(["Shipped", "+10", "10"]);
    expect(within(rows[1]).getByRole("link", { name: "Packing slip" })).toHaveAttribute("href", `/fulfillment/packing-slips/${SLIP}`);
    expect(calls(MOVEMENTS)[0].url.searchParams.get("limit")).toBe(String(MOVEMENT_LIMIT));
    // Without ReturnablePackagingMaintain there is no correction form.
    expect(screen.queryByRole("button", { name: "Correct balance" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by customer, type and movement, and validates the filter", async () => {
    server.handler = (url) => (url.pathname === MOVEMENTS ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/returnable-movements");
    expect(await screen.findByText("No returnable packaging movements yet")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Customer id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    expect(await screen.findByText(/Enter the customer id/)).toBeInTheDocument();
    expect(calls(MOVEMENTS)).toHaveLength(1);

    fireEvent.change(screen.getByLabelText("Customer id"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Container type"), { target: { value: "PALLET" } });
    fireEvent.change(screen.getByLabelText("Movement"), { target: { value: "RETURNED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    expect(await screen.findByText("No movements match this filter")).toBeInTheDocument();
    const params = calls(MOVEMENTS).at(-1)!.url.searchParams;
    expect([params.get("customerId"), params.get("containerType"), params.get("kind")]).toEqual([CUSTOMER, "PALLET", "RETURNED"]);

    fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(await screen.findByText("No returnable packaging movements yet")).toBeInTheDocument();
    expect(calls(MOVEMENTS).at(-1)!.url.searchParams.has("kind")).toBe(false);
  });

  it("shows the error state and retries", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname !== MOVEMENTS) return undefined;
      return fail ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, { items: [movement("0195f0a0-0000-7000-8000-0000000000a1")] });
    };
    renderAt("/fulfillment/returnable-movements");
    expect((await findAlert()).textContent).toContain("Could not load the movements");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await findTable({ name: "Returnable packaging movements" });
  });
});

describe("Correct a returnable balance", () => {
  it("validates, shows the refusal, and saves with a fresh Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname === MOVEMENTS) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/container-types") return json(200, { items: [pallet, carton] });
      if (url.pathname === ADJUST && init.method === "POST") {
        const body = JSON.parse(String(init.body)) as { delta: string };
        if (body.delta === "-9") return json(422, { code: "FUL-0015", detail: "returnable packaging balance would become negative" });
        return json(201, movement("0195f0a0-0000-7000-8000-0000000000a9", { kind: "ADJUSTED", qty: body.delta, balanceAfter: "12", reason: "Opening balance", reference: null }));
      }
      return undefined;
    };
    renderAt("/fulfillment/returnable-movements", maintain);
    const form = await screen.findByRole("button", { name: "Correct balance" });
    await waitFor(() => expect(within(screen.getByLabelText("Container type", { selector: "#adj-type" })).getByRole("option", { name: "PALLET" })).toBeInTheDocument());
    // Only returnable types are offered.
    expect(within(screen.getByLabelText("Container type", { selector: "#adj-type" })).queryByRole("option", { name: "C12" })).toBeNull();

    fireEvent.click(form);
    expect(await screen.findByText(/Enter the customer id \(a UUID\)\./)).toBeInTheDocument();
    expect(screen.getByText(/Choose the returnable container type/)).toBeInTheDocument();
    expect(screen.getByText(/not zero; negative takes containers off/)).toBeInTheDocument();
    expect(screen.getByText(/Give the reason, 3 to 200 characters/)).toBeInTheDocument();
    expect(calls(ADJUST, "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Customer id", { selector: "#adj-customer" }), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Container type", { selector: "#adj-type" }), { target: { value: "PALLET" } });
    fireEvent.change(screen.getByLabelText("Change in containers"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "Lost" } });
    fireEvent.click(form);
    expect(await screen.findByText(/not zero; negative takes containers off/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Change in containers"), { target: { value: "-9" } });
    fireEvent.click(form);
    expect((await findAlert()).textContent).toContain("FUL-0015");

    fireEvent.change(screen.getByLabelText("Change in containers"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: " Opening balance " } });
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: " sheet 9 " } });
    fireEvent.click(form);
    expect((await findStatus()).textContent).toContain("now holds 12 PALLET");
    const posts = calls(ADJUST, "POST");
    expect(posts.at(-1)?.body).toEqual({ customerId: CUSTOMER, containerType: "PALLET", delta: "12", reason: "Opening balance", reference: "sheet 9" });
    expect(idempotencyKey(posts[0])).not.toBe(idempotencyKey(posts[1]));
    // The list reloads after a save.
    await waitFor(() => expect(calls(MOVEMENTS).length).toBeGreaterThan(1));
  });

  it("hides the form from a caller who receives returns but cannot correct balances", async () => {
    server.handler = (url) => (url.pathname === MOVEMENTS ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/returnable-movements", ["WarehouseView", "WarehouseReceive"]);
    await screen.findByText("No returnable packaging movements yet");
    expect(screen.queryByRole("button", { name: "Correct balance" })).toBeNull();
  });
});

import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ADMIN = ["pricing.view", "pricing.admin"];

function load() {
  fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
  fireEvent.click(screen.getByRole("button", { name: "Load parameters" }));
}

describe("Pricing parameters", () => {
  it("starts empty and validates the legal entity id", async () => {
    const { container } = renderAt("/pricing/parameters");
    expect(screen.getByText("No legal entity loaded")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load parameters" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("loads the defaults and saves four eyes at the version read", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT") return json(200, { legalEntityId: LE, journalFourEyes: true, version: 1, updatedBy: "ana" });
      return json(200, { legalEntityId: LE, journalFourEyes: false, version: 0 });
    };
    const { container } = renderAt("/pricing/parameters", ADMIN);
    load();
    const box = await screen.findByLabelText("Four eyes on journals");
    expect(screen.getByText("Defaults, nothing saved yet.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(box).not.toBeChecked();
    fireEvent.click(box);
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({
      legalEntityId: LE,
      journalFourEyes: true,
      pricingDateBasis: "SHIP_DATE",
      totalDiscountBasis: "AFTER_LINE_DISC",
      smartRoundingBands: [{ upTo: "10", ending: "0.99" }, { upTo: "100", ending: "0.95" }, { ending: "0" }],
      priceSearchMode: "FIRST_MATCH",
      smallOrderThresholds: {},
      version: 0,
    });
    expect(idempotencyKey(put)).toBeTruthy();
    expect(screen.getByText("Version 1, saved by ana")).toBeInTheDocument();
  });

  it("saves the date basis, total discount basis and an edited rounding table", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT") return json(200, { ...(init.body ? JSON.parse(String(init.body)) : {}), version: 5 });
      return json(200, {
        legalEntityId: LE,
        journalFourEyes: false,
        pricingDateBasis: "ORDER_DATE",
        totalDiscountBasis: "GROSS",
        smartRoundingBands: [{ upTo: "20", ending: "0.5" }, { ending: "0" }],
        version: 4,
      });
    };
    const { container } = renderAt("/pricing/parameters", ADMIN);
    load();
    expect(await screen.findByLabelText("Pricing date basis")).toHaveValue("ORDER_DATE");
    expect(screen.getByLabelText("Total discount basis")).toHaveValue("GROSS");
    expect(screen.getByLabelText("Band 1: below")).toHaveValue("20");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Pricing date basis"), { target: { value: "DELIVERY_DATE" } });
    fireEvent.change(screen.getByLabelText("Total discount basis"), { target: { value: "AFTER_LINE_DISC" } });
    fireEvent.click(screen.getByRole("button", { name: "Add band" }));
    fireEvent.change(screen.getByLabelText("Band 2: below"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Band 2: ending"), { target: { value: "0.95" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({
      legalEntityId: LE,
      journalFourEyes: false,
      pricingDateBasis: "DELIVERY_DATE",
      totalDiscountBasis: "AFTER_LINE_DISC",
      smartRoundingBands: [{ upTo: "20", ending: "0.5" }, { upTo: "50", ending: "0.95" }, { ending: "0" }],
      priceSearchMode: "FIRST_MATCH",
      smallOrderThresholds: {},
      version: 4,
    });
    fireEvent.click(screen.getByRole("button", { name: "Remove band 2" }));
    expect(screen.queryByLabelText("Band 3: below")).toBeNull();
  });

  it("shows a refused band on its own field", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT")
        return json(422, { code: "COM-0001", detail: "validation failed", errors: { "smartRoundingBands[1].upTo": "must be greater than the band before" } });
      return json(200, { legalEntityId: LE, journalFourEyes: false, pricingDateBasis: "SHIP_DATE", totalDiscountBasis: "AFTER_LINE_DISC", smartRoundingBands: [{ upTo: "10", ending: "0.99" }, { upTo: "100", ending: "0.95" }, { ending: "0" }], version: 2 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    fireEvent.change(await screen.findByLabelText("Band 2: below"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("must be greater than the band before")).toBeInTheDocument();
    expect(screen.getByLabelText("Band 2: below")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Band 1: below")).not.toHaveAttribute("aria-invalid");
  });

  it("shows the rounding table read-only without pricing.admin", async () => {
    server.handler = () => json(200, { legalEntityId: LE, journalFourEyes: false, pricingDateBasis: "SHIP_DATE", totalDiscountBasis: "GROSS", smartRoundingBands: [{ ending: "0" }], version: 1 });
    renderAt("/pricing/parameters", ["pricing.view"]);
    load();
    expect(await screen.findByLabelText("Band 1: ending")).toBeDisabled();
    expect(screen.getByLabelText("Total discount basis")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Add band" })).toBeNull();
  });

  it("reads without pricing.admin but offers no save", async () => {
    server.handler = () => json(200, { legalEntityId: LE, journalFourEyes: true, version: 3, updatedBy: "ana" });
    renderAt("/pricing/parameters", ["pricing.view"]);
    load();
    expect(await screen.findByLabelText("Four eyes on journals")).toBeChecked();
    expect(screen.getByLabelText("Four eyes on journals")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save parameters" })).toBeNull();
    expect(screen.getByText("Changing them needs the pricing.admin duty.")).toBeInTheDocument();
  });

  it("retries a failed save with the same key, and a changed value with a new one", async () => {
    let fail = true;
    server.handler = (_url, init) => {
      if (init.method === "PUT") return fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { legalEntityId: LE, journalFourEyes: false, version: 2 });
      return json(200, { legalEntityId: LE, journalFourEyes: true, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    fireEvent.click(await screen.findByRole("button", { name: "Save parameters" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    fail = false;
    fireEvent.click(screen.getByLabelText("Four eyes on journals"));
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const keys = server.calls.filter((c) => c.method === "PUT").map(idempotencyKey);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[1]);
  });

  it("shows the service error, such as a stale version", async () => {
    server.handler = (_url, init) => {
      if (init.method === "PUT") return json(412, { code: "conflict_version", detail: "parameters version does not match" });
      return json(200, { legalEntityId: LE, journalFourEyes: false, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    fireEvent.click(await screen.findByRole("button", { name: "Save parameters" }));
    await waitFor(async () => expect(await findAlert()).toHaveTextContent("parameters version does not match (conflict_version)"));
  });

  it("shows a read failure", async () => {
    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/pricing/parameters");
    load();
    expect(await findAlert()).toHaveTextContent("internal error (COM-0500)");
  });
  it("saves the price search mode and the small order thresholds", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT") return json(200, { ...(init.body ? JSON.parse(String(init.body)) : {}), version: 3 });
      return json(200, { legalEntityId: LE, journalFourEyes: false, pricingDateBasis: "SHIP_DATE", totalDiscountBasis: "AFTER_LINE_DISC", smartRoundingBands: [{ ending: "0" }], priceSearchMode: "SEARCH_NEXT", smallOrderThresholds: { USD: "100", EUR: "0" }, version: 2 });
    };
    const { container } = renderAt("/pricing/parameters", ADMIN);
    load();
    expect(await screen.findByLabelText("Price search mode")).toHaveValue("SEARCH_NEXT");
    expect(screen.getByLabelText("Threshold 1: currency")).toHaveValue("EUR");
    expect(screen.getByLabelText("Threshold 2: amount")).toHaveValue("100");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Price search mode"), { target: { value: "FIRST_MATCH" } });
    fireEvent.change(screen.getByLabelText("Threshold 2: amount"), { target: { value: "150.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove threshold 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Add threshold" }));
    fireEvent.change(screen.getByLabelText("Threshold 2: currency"), { target: { value: "gbp" } });
    fireEvent.change(screen.getByLabelText("Threshold 2: amount"), { target: { value: "75" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toMatchObject({ priceSearchMode: "FIRST_MATCH", smallOrderThresholds: { USD: "150.50", GBP: "75" }, version: 2 });
  });

  it("shows a refused threshold on its own currency, and no threshold controls without pricing.admin", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT")
        return json(422, { code: "COM-0001", detail: "validation failed", errors: { "smallOrderThresholds[USD]": "must be a decimal of at least 0" } });
      return json(200, { legalEntityId: LE, journalFourEyes: false, smartRoundingBands: [{ ending: "0" }], smallOrderThresholds: { USD: "-5" }, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    await screen.findByLabelText("Threshold 1: currency");
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("must be a decimal of at least 0")).toBeInTheDocument();
    expect(screen.getByLabelText("Threshold 1: currency")).toHaveAttribute("aria-invalid", "true");
  });

  it("refuses two thresholds of one currency before sending anything", async () => {
    server.handler = (url) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      return json(200, { legalEntityId: LE, journalFourEyes: false, smartRoundingBands: [{ ending: "0" }], smallOrderThresholds: { USD: "100" }, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    await screen.findByLabelText("Threshold 1: currency");
    fireEvent.click(screen.getByRole("button", { name: "Add threshold" }));
    fireEvent.change(screen.getByLabelText("Threshold 2: currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Threshold 2: amount"), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await findAlert()).toHaveTextContent("The currency USD has more than one small order threshold.");
    expect(screen.getByLabelText("Threshold 2: currency")).toHaveAttribute("aria-invalid", "true");
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("reads the thresholds without pricing.admin and offers no edit", async () => {
    server.handler = () => json(200, { legalEntityId: LE, journalFourEyes: false, smartRoundingBands: [{ ending: "0" }], priceSearchMode: "SEARCH_NEXT", smallOrderThresholds: { USD: "100" }, version: 1 });
    renderAt("/pricing/parameters", ["pricing.view"]);
    load();
    expect(await screen.findByLabelText("Threshold 1: amount")).toBeDisabled();
    expect(screen.getByLabelText("Price search mode")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Add threshold" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove threshold 1" })).toBeNull();
  });
});

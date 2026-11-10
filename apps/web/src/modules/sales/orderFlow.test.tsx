import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import { OrderFlowPanel, orderNextStep, orderStages, type OrderFlow } from "./documentFlow";
import { OrderToCashPage } from "./OrderToCashPage";
import type { Order } from "./api/client";

useFakeServer();

const order = (status: string) => ({ id: "o1", orderNumber: "SO-1", customerId: "c1", legalEntityId: "le", status }) as unknown as Order;
const slip = (status: string) => ({ id: "s1", packingSlipNumber: "PS-1", status, reversalOfId: null }) as never;
const invoice = (status: string, number = "INV-1") => ({ id: "i1", number, status }) as never;
const item = (openAmount: string) => ({ documentNumber: "INV-1", openAmount, currency: "USD" }) as never;
const flow = (p: Partial<OrderFlow>): OrderFlow => ({ order: order("CONFIRMED"), slips: [], invoices: [], openItems: [], ...p });

describe("order-to-cash next step", () => {
  it("walks the order through each step", () => {
    expect(orderNextStep(flow({ order: order("OPEN") }))?.title).toBe("confirm the order");
    expect(orderNextStep(flow({}))?.title).toBe("ship the order");
    expect(orderNextStep(flow({ slips: [slip("POSTED")] }))?.title).toBe("invoice the shipment");
    expect(orderNextStep(flow({ slips: [slip("INVOICED")], invoices: [invoice("PROPOSAL", "")] }))?.title).toBe("post the invoice");
    const posted = flow({ slips: [slip("INVOICED")], invoices: [invoice("POSTED")], openItems: [item("50.00")] });
    expect(orderNextStep(posted)?.title).toBe("record the payment");
    expect(orderNextStep({ ...posted, openItems: [item("0.00")] })).toBeNull();
  });

  it("offers nothing for a cancelled order and carries the customer into the link", () => {
    expect(orderNextStep(flow({ order: order("CANCELLED") }))).toBeNull();
    expect(orderNextStep(flow({ slips: [slip("POSTED")] }))?.to).toContain("customerId=c1");
  });

  it("marks the page's own document and a stage the user cannot read", () => {
    const stages = orderStages(flow({ slips: [slip("POSTED")], invoices: null }), { kind: "slip", id: "s1" });
    expect(stages[1].documents[0].current).toBe(true);
    expect(stages[2].unavailable).toBe(true);
    expect(stages[0].documents[0].label).toBe("Sales order SO-1");
  });
});

describe("order flow panel", () => {
  it("shows the documents with their status and the next step", async () => {
    server.handler = (url) => {
      const p = url.pathname;
      if (p === "/api/v1/sales/orders/o1") return json(200, order("CONFIRMED"));
      if (p === "/api/v1/fulfillment/packing-slips") return json(200, { items: [slip("POSTED")] });
      if (p === "/api/v1/billing/invoices") return json(200, { items: [] });
      if (p === "/api/v1/receivables/open-items") return json(200, { items: [] });
      return undefined;
    };
    render(
      <MemoryRouter>
        <OrderFlowPanel anchor={{ kind: "order", id: "o1" }} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("region", { name: "Document flow of this sales order" })).toBeInTheDocument();
    expect(screen.getByText("Packing slip PS-1")).toBeInTheDocument();
    expect(screen.getByText("Next: invoice the shipment")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Invoice shipments" })).toHaveAttribute("href", "/billing/proposals/new?customerId=c1");
  });

  it("says so when the related documents cannot be read", async () => {
    server.handler = () => json(500, { code: "X", detail: "down" });
    render(
      <MemoryRouter>
        <OrderFlowPanel anchor={{ kind: "order", id: "o1" }} />
      </MemoryRouter>,
    );
    expect(await screen.findByText("Related documents are not available")).toBeInTheDocument();
  });
});

describe("guided order to cash page", () => {
  it("shows the five steps with their counts", async () => {
    server.handler = (url) => {
      const p = url.pathname;
      if (p === "/api/v1/sales/orders") return json(200, { items: [{}, {}, {}] });
      if (p === "/api/v1/fulfillment/packing-slips") return json(200, { items: [{}] });
      if (p === "/api/v1/billing/invoices") return json(200, { items: [] });
      if (p === "/api/v1/receivables/open-items") return json(200, { items: [{}, {}] });
      return undefined;
    };
    const duties = ["sales.order.read", "sales.order.write", "fulfillment.warehouse.view", "billing.invoice.view", "billing.invoice.maintain", "receivables.view"];
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <PrincipalProvider duties={duties}>
          <MemoryRouter>
            <OrderToCashPage />
          </MemoryRouter>
        </PrincipalProvider>
      </QueryClientProvider>,
    );
    const steps = await screen.findAllByRole("listitem");
    expect(steps).toHaveLength(5);
    expect(await screen.findByText("1 shipments not invoiced")).toBeInTheDocument();
    expect(await screen.findByText("2 open receivables")).toBeInTheDocument();
    expect(screen.getByText("0 invoice proposals to post")).toBeInTheDocument();
  });

  it("locks a step and its count when the user lacks the duty", async () => {
    server.handler = () => json(200, { items: [] });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <PrincipalProvider duties={["sales.order.read"]}>
          <MemoryRouter>
            <OrderToCashPage />
          </MemoryRouter>
        </PrincipalProvider>
      </QueryClientProvider>,
    );
    expect((await screen.findAllByText("Count not available")).length).toBeGreaterThanOrEqual(3);
    expect(screen.getAllByText("Needs a different role").length).toBeGreaterThanOrEqual(3);
  });
});

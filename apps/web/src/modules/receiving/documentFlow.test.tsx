import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import { ProcureToPayPage } from "../purchasing/ProcureToPayPage";
import type { PurchaseOrder } from "../purchasing/api/client";
import { PurchaseFlowPanel, purchaseNextStep, purchaseStages, type PurchaseFlow } from "./documentFlow";

useFakeServer();

const po = (approvalStatus: string) => ({ id: "po1", number: "PO-1", vendorId: "v1", legalEntityId: "le1", approvalStatus }) as unknown as PurchaseOrder;
const receipt = (state: string) => ({ id: "r1", receiptNumber: "RCV-1", state, receiptDate: "2026-11-10" });
const invoice = { id: "i1", invoiceNumber: "VI-1", status: "POSTED", invoiceDate: "2026-11-11", total: "10.00", currency: "USD" };
const flow = (p: Partial<PurchaseFlow> & { receipts?: ReturnType<typeof receipt>[]; invoices?: (typeof invoice)[] }): PurchaseFlow => ({
  related: { purchaseOrderId: "po1", receipts: p.receipts ?? [], invoices: p.invoices ?? [], truncated: false },
  order: po("CONFIRMED"),
  openItems: [],
  ...(p.order !== undefined ? { order: p.order } : {}),
  ...(p.openItems !== undefined ? { openItems: p.openItems } : {}),
});
const openItem = (extra: object) => ({ sourceInvoiceId: "i1", status: "OPEN", openAmount: "10.00", paymentHold: false, ...extra }) as never;

describe("procure-to-pay next step", () => {
  it("walks the purchase through each step", () => {
    expect(purchaseNextStep(flow({ order: po("DRAFT") }))?.title).toBe("approve and confirm the purchase order");
    const receive = purchaseNextStep(flow({}));
    expect(receive?.title).toBe("receive the goods");
    expect(receive?.to).toBe("/receiving/receipts/new?purchaseOrderId=po1&vendorId=v1");
    expect(purchaseNextStep(flow({ receipts: [receipt("ARRIVED")] }))?.title).toBe("register the goods received");
    expect(purchaseNextStep(flow({ receipts: [receipt("REGISTERED")] }))?.title).toBe("post the receipt");
    expect(purchaseNextStep(flow({ receipts: [receipt("POSTED")] }))?.title).toBe("enter the vendor invoice");
    const posted = flow({ receipts: [receipt("POSTED")], invoices: [invoice], openItems: [openItem({})] });
    expect(purchaseNextStep(posted)?.title).toBe("pay the vendor");
    expect(purchaseNextStep({ ...posted, openItems: [openItem({ proposalId: "p1" })] })).toBeNull();
    expect(purchaseNextStep({ ...posted, openItems: [openItem({ status: "SETTLED", openAmount: "0.00" })] })).toBeNull();
  });

  it("shows payment status and marks the page's own document", () => {
    const stages = purchaseStages(flow({ receipts: [receipt("POSTED")], invoices: [invoice], openItems: [openItem({ paymentHold: true })] }), { kind: "receipt", id: "r1" });
    expect(stages[1].documents[0].current).toBe(true);
    expect(stages[3].documents[0].status).toBe("On hold");
    expect(purchaseStages(flow({ invoices: [invoice], openItems: null }), { kind: "po", id: "po1" })[3].unavailable).toBe(true);
  });
});

describe("purchase flow panel", () => {
  const renderPanel = (duties: string[]) =>
    render(
      <PrincipalProvider duties={duties}>
        <MemoryRouter>
          <PurchaseFlowPanel anchor={{ kind: "receipt", id: "r1" }} />
        </MemoryRouter>
      </PrincipalProvider>,
    );

  it("is not shown without the duty and asks nothing", () => {
    renderPanel(["receiving.receipt.maintain"]);
    expect(screen.queryByText("Document flow")).not.toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("shows the related documents and the next step", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/receiving/related-documents") return json(200, { purchaseOrderId: "po1", receipts: [receipt("POSTED")], invoices: [], truncated: false });
      if (url.pathname === "/api/v1/procurement/purchase-orders/po1") return json(200, po("CONFIRMED"));
      return undefined;
    };
    renderPanel(["receiving.document-flow.read"]);
    expect(await screen.findByText("Purchase order PO-1")).toBeInTheDocument();
    expect(screen.getByText("Receipt RCV-1")).toBeInTheDocument();
    expect(screen.getByText("Next: enter the vendor invoice")).toBeInTheDocument();
  });
});

describe("guided procure-to-pay page", () => {
  it("shows five steps with counts, and a step the user cannot do is locked", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/procurement/purchase-orders") return json(200, { items: [{}, {}] });
      return json(200, { items: [] });
    };
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <PrincipalProvider duties={["procurement.purchase-order.inquire", "procurement.purchase-order.maintain"]}>
          <MemoryRouter>
            <ProcureToPayPage />
          </MemoryRouter>
        </PrincipalProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findAllByRole("listitem")).toHaveLength(5);
    expect(await screen.findByText("2 purchase orders to confirm")).toBeInTheDocument();
    expect(screen.getAllByText("Count not available").length).toBe(3);
    expect(screen.getAllByText("Needs a different role").length).toBe(4);
    expect(screen.getByRole("link", { name: "New purchase order" })).toBeInTheDocument();
  });
});

import { createElement } from "react";
import type { PassageModule } from "../registry";
import { GrirPage } from "./GrirPage";
import { InvoiceDetailPage } from "./InvoiceDetailPage";
import { InvoicesPage } from "./InvoicesPage";
import { PostInvoicePage } from "./PostInvoicePage";

const apInvoices: PassageModule = {
  id: "ap-invoices",
  title: "AP invoices",
  description: "See goods received and not yet invoiced, post vendor invoices against them and look up what was posted.",
  routes: [
    { path: "/ap-invoices", element: createElement(InvoicesPage), handle: { title: "Vendor invoices" } },
    { path: "/ap-invoices/grir", element: createElement(GrirPage), handle: { title: "GR/IR open items" } },
    { path: "/ap-invoices/post", element: createElement(PostInvoicePage), handle: { title: "Post vendor invoice" } },
    { path: "/ap-invoices/:id", element: createElement(InvoiceDetailPage), handle: { title: "Vendor invoice" } },
  ],
  nav: [
    { label: "Vendor invoices", to: "/ap-invoices", duty: "receiving.ap.inquire" },
    { label: "GR/IR open items", to: "/ap-invoices/grir", duty: "receiving.ap.inquire" },
    { label: "Post vendor invoice", to: "/ap-invoices/post", duty: "receiving.vendor-invoice.post" },
  ],
};

export default apInvoices;

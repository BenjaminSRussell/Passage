import { createElement } from "react";
import type { PassageModule } from "../registry";
import { GrirPage } from "./GrirPage";
import { PostInvoicePage } from "./PostInvoicePage";

const apInvoices: PassageModule = {
  id: "ap-invoices",
  title: "AP invoices",
  description: "See goods received and not yet invoiced, and post vendor invoices against them.",
  routes: [
    { path: "/ap-invoices/grir", element: createElement(GrirPage), handle: { title: "GR/IR open items" } },
    { path: "/ap-invoices/post", element: createElement(PostInvoicePage), handle: { title: "Post vendor invoice" } },
  ],
  nav: [
    { label: "GR/IR open items", to: "/ap-invoices/grir", duty: "APInquire" },
    { label: "Post vendor invoice", to: "/ap-invoices/post", duty: "VendorInvoicePost" },
  ],
};

export default apInvoices;

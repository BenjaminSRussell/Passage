import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AdminHome, AlertInbox, AuditViewer, DocumentsPage, ImportExportPage, SequencePage, TextsPage } from "./pages";

const admin: PassageModule = {
  id: "admin",
  title: "Admin",
  routes: [
    { path: "/admin", element: createElement(AdminHome) },
    { path: "/admin/audit", element: createElement(AuditViewer) },
    { path: "/admin/sequences", element: createElement(SequencePage) },
    { path: "/admin/alerts", element: createElement(AlertInbox) },
    { path: "/admin/import", element: createElement(ImportExportPage) },
    { path: "/admin/documents", element: createElement(DocumentsPage) },
    { path: "/admin/texts", element: createElement(TextsPage) },
  ],
  nav: [
    { label: "Overview", to: "/admin", duty: "audit.read" },
    { label: "Audit", to: "/admin/audit", duty: "audit.read" },
    { label: "Sequences", to: "/admin/sequences", duty: "numbering.write" },
    { label: "Alerts", to: "/admin/alerts", duty: "audit.read" },
    { label: "Import / export", to: "/admin/import" },
    { label: "Documents", to: "/admin/documents" },
    { label: "Standard texts", to: "/admin/texts" },
  ],
};

export default admin;

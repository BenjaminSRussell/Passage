import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EffectivePermissionsPage } from "./security/EffectivePermissionsPage";
import { LegalEntitiesPage } from "./security/LegalEntitiesPage";
import { RoleAssignmentsPage } from "./security/RoleAssignmentsPage";
import { RoleDetailPage, RolesPage } from "./security/RolesPage";
import { UserDetailPage, UsersPage } from "./security/UsersPage";
import { AdminHome, AlertInbox, AuditViewer, DocumentsPage, ImportExportPage, SequencePage, TextsPage } from "./pages";

const admin: PassageModule = {
  id: "admin",
  title: "Admin",
  description: "Numbering, audit, alerts, imports, documents and security.",
  routes: [
    { path: "/admin", element: createElement(AdminHome) },
    { path: "/admin/audit", element: createElement(AuditViewer) },
    { path: "/admin/sequences", element: createElement(SequencePage) },
    { path: "/admin/alerts", element: createElement(AlertInbox) },
    { path: "/admin/import", element: createElement(ImportExportPage) },
    { path: "/admin/documents", element: createElement(DocumentsPage) },
    { path: "/admin/texts", element: createElement(TextsPage) },
    { path: "/admin/security/permissions", element: createElement(EffectivePermissionsPage), handle: { title: "Effective permissions" } },
    { path: "/admin/security/assignments", element: createElement(RoleAssignmentsPage), handle: { title: "Role assignments" } },
    { path: "/admin/security/legal-entities", element: createElement(LegalEntitiesPage), handle: { title: "Legal entities" } },
    { path: "/admin/security/users", element: createElement(UsersPage), handle: { title: "Users" } },
    { path: "/admin/security/users/:userId", element: createElement(UserDetailPage), handle: { title: "User" } },
    { path: "/admin/security/roles", element: createElement(RolesPage), handle: { title: "Role catalogue" } },
    { path: "/admin/security/roles/:name", element: createElement(RoleDetailPage), handle: { title: "Role" } },
  ],
  nav: [
    { label: "Overview", to: "/admin", duty: "audit.read" },
    { label: "Audit", to: "/admin/audit", duty: "audit.read" },
    { label: "Sequences", to: "/admin/sequences", duty: "numbering.write" },
    { label: "Alerts", to: "/admin/alerts", duty: "audit.read" },
    { label: "Import / export", to: "/admin/import" },
    { label: "Documents", to: "/admin/documents" },
    { label: "Standard texts", to: "/admin/texts" },
    { label: "Effective permissions", to: "/admin/security/permissions", duty: "View effective permissions" },
    { label: "Role assignments", to: "/admin/security/assignments", duty: "Maintain security roles" },
    { label: "Legal entities", to: "/admin/security/legal-entities", duty: "Maintain legal entities" },
    { label: "Users", to: "/admin/security/users", duty: "Maintain users" },
    { label: "Role catalogue", to: "/admin/security/roles", duty: "Maintain security roles" },
  ],
};

export default admin;

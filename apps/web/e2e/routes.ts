/**
 * Every routable page of the shell: shell pages, each `nav.to` in
 * `src/modules/*\/module.ts`, one product detail and a 404 path.
 * Keep in sync with the module nav lists.
 */
export const ROUTES: readonly string[] = [
  "/",
  "/features",
  "/whats-new",
  "/design-system",
  "/metadata/Vendor",
  "/workflow",
  "/workflow/inbox",
  "/workflow/designer",
  "/workflow/jobs",
  "/workflow/templates",
  "/product",
  "/product/items",
  "/product/released",
  "/product/BOLT",
  "/engineering",
  "/engineering/where-used",
  "/admin",
  "/admin/audit",
  "/admin/sequences",
  "/admin/alerts",
  "/admin/import",
  "/admin/documents",
  "/admin/texts",
  "/payables",
  "/party",
  "/party/new",
  "/party/tax-registrations",
  "/party/duplicates",
  "/party/pending-changes",
  "/no-such-page",
];

import { createElement } from "react";
import apInvoices from "./module";
import { PrincipalProvider } from "../../shell/principal";
import { renderRoutes } from "../../shell/testing/fakeServer";

export { idempotencyKey, json, pageViolations, server, useFakeServer } from "../../shell/testing/fakeServer";

export const ALL_DUTIES = ["receiving.receipt.maintain", "procurement.purchase-order.inquire", "ledger.journal.read"];

export const renderAt = (path: string, duties: readonly string[] = ALL_DUTIES) =>
  renderRoutes(
    apInvoices.routes.map((route) => ({ ...route, element: createElement(PrincipalProvider, { duties, children: route.element }) })),
    path,
  );

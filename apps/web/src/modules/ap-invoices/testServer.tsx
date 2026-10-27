import apInvoices from "./module";
import { renderRoutes } from "../receiving/testServer";

export { idempotencyKey, json, pageViolations, server, useFakeServer } from "../receiving/testServer";

export const renderAt = (path: string) => renderRoutes(apInvoices.routes, path);

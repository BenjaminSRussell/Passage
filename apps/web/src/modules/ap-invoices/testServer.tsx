import apInvoices from "./module";
import { renderRoutes } from "../../shell/testing/fakeServer";

export { idempotencyKey, json, pageViolations, server, useFakeServer } from "../../shell/testing/fakeServer";

export const renderAt = (path: string) => renderRoutes(apInvoices.routes, path);

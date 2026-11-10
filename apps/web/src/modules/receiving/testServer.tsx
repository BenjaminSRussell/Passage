import receiving from "./module";
import { renderRoutes } from "../../shell/testing/fakeServer";
import { installLookupStub } from "../purchasing/lookupStub";

export { idempotencyKey, json, pageViolations, server, useFakeServer } from "../../shell/testing/fakeServer";

export const renderAt = (path: string) => {
  installLookupStub();
  return renderRoutes(receiving.routes, path);
};

import "@testing-library/jest-dom/vitest";
import { act, configure, getConfig } from "@testing-library/react";
import { beforeEach } from "vitest";
import { clearResolvedOptions, setLookupFetcher } from "./shell/lookup/entities";

beforeEach(() => {
  clearResolvedOptions();
  setLookupFetcher(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
});

// React 19 schedules more work asynchronously: the 1 s default for findBy*/waitFor
// times out on a loaded runner, and passive effects from the render a findBy* waited
// for (e.g. "reset the draft when data loads") can still be pending when the test
// acts next. Flush them after every async query so tests act on a settled page.
const asyncWrapper = getConfig().asyncWrapper;
configure({
  asyncUtilTimeout: 5000,
  asyncWrapper: async (cb) => {
    const result = await asyncWrapper(cb);
    await act(async () => {});
    return result;
  },
});

beforeEach(() => {
  // Node-environment suites (e.g. plainLanguage.test.ts) have no window.
  if (typeof window !== "undefined") window.localStorage.clear();
});

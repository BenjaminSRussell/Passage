import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { DeviceHost } from "./modules/mobile-flows/DeviceHost";
import { loadModules } from "./modules/registry";
import { Shell } from "./shell/Shell";
import { HomePage } from "./shell/pages/HomePage";
import { NotesPage } from "./shell/pages/NotesPage";
import { NotFoundPage } from "./shell/pages/NotFoundPage";
import "./shell/theme.css";

const demo = import.meta.env.MODE === "demo";
// The Pages demo serves under a sub-path (vite `base`); everywhere else BASE_URL is "/".
const basename = import.meta.env.BASE_URL.replace(/\/$/, "") || "/";

const modules = loadModules();
const router = createBrowserRouter(
  [
  ...(import.meta.env.DEV ? [{ path: "/device", element: <DeviceHost /> }] : []),
  {
    path: "/",
    element: <Shell modules={modules} />,
    children: [
      { index: true, element: <HomePage modules={modules} /> },
      { path: "notes", element: <NotesPage /> },
      ...modules.flatMap((m) => m.routes),
      { path: "*", element: <NotFoundPage /> },
    ],
  },
  ],
  { basename },
);

async function start() {
  // The demo build answers /api/ calls in the browser; the import is dropped from every other build.
  const DemoBanner = demo ? (await import("./demo/install"), (await import("./demo/DemoBanner")).DemoBanner) : null;
  createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
        {DemoBanner ? <DemoBanner /> : null}
      </QueryClientProvider>
    </React.StrictMode>,
  );
}

void start();

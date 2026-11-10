import React, { useMemo, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { DeviceHost } from "./modules/mobile-flows/DeviceHost";
import { loadModules } from "./modules/registry";
import { Shell } from "./shell/Shell";
import { AuthRoot } from "./shell/auth/AuthRoot";
import { bootstrapAuth } from "./shell/auth/auth";
import { AllFeaturesPage } from "./shell/pages/AllFeaturesPage";
import { HomePage } from "./shell/pages/HomePage";
import { WhatsNewPage } from "./shell/pages/WhatsNewPage";
import { NotesPage } from "./shell/pages/NotesPage";
import { NotFoundPage } from "./shell/pages/NotFoundPage";
import "./shell/theme.css";

const demo = import.meta.env.MODE === "demo";
// The Pages demo serves under a sub-path (vite `base`); everywhere else BASE_URL is "/".
const basename = import.meta.env.BASE_URL.replace(/\/$/, "") || "/";

const modules = loadModules();

function App({ duties, identity, banner }: { duties?: readonly string[]; identity?: string; banner?: ReactNode }) {
  const router = useMemo(
    () =>
      createBrowserRouter(
        [
          ...(import.meta.env.DEV ? [{ path: "/device", element: <DeviceHost /> }] : []),
          {
            path: "/",
            element: <Shell modules={modules} duties={duties} identity={identity} />,
            children: [
              { index: true, element: <HomePage /> },
              { path: "notes", element: <NotesPage /> },
              { path: "features", element: <AllFeaturesPage /> },
              { path: "whats-new", element: <WhatsNewPage /> },
              ...modules.flatMap((m) => m.routes),
              { path: "*", element: <NotFoundPage /> },
            ],
          },
        ],
        { basename },
      ),
    [duties, identity],
  );
  const client = useMemo(() => new QueryClient(), []);
  return (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
      {banner}
    </QueryClientProvider>
  );
}

async function start() {
  // The demo build answers /api/ calls in the browser and has no sign-in; the import is dropped from every other build.
  const DemoBanner = demo ? (await import("./demo/install"), (await import("./demo/DemoBanner")).DemoBanner) : null;
  const auth = bootstrapAuth();
  createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
      <AuthRoot auth={auth} requireAuth={import.meta.env.PROD && !demo}>
        {(duties, identity) => <App duties={duties} identity={identity} banner={DemoBanner ? <DemoBanner /> : null} />}
      </AuthRoot>
    </React.StrictMode>,
  );
}

void start();

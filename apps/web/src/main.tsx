import React, { useMemo } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { DeviceHost } from "./modules/mobile-flows/DeviceHost";
import { loadModules } from "./modules/registry";
import { Shell } from "./shell/Shell";
import { AuthRoot } from "./shell/auth/AuthRoot";
import { bootstrapAuth } from "./shell/auth/auth";
import { HomePage } from "./shell/pages/HomePage";
import { NotesPage } from "./shell/pages/NotesPage";
import { NotFoundPage } from "./shell/pages/NotFoundPage";
import "./shell/theme.css";

const modules = loadModules();

function App({ duties }: { duties?: readonly string[] }) {
  const router = useMemo(
    () =>
      createBrowserRouter([
        ...(import.meta.env.DEV ? [{ path: "/device", element: <DeviceHost /> }] : []),
        {
          path: "/",
          element: <Shell modules={modules} duties={duties} />,
          children: [
            { index: true, element: <HomePage modules={modules} /> },
            { path: "notes", element: <NotesPage /> },
            ...modules.flatMap((m) => m.routes),
            { path: "*", element: <NotFoundPage /> },
          ],
        },
      ]),
    [duties],
  );
  const client = useMemo(() => new QueryClient(), []);
  return (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}

const auth = bootstrapAuth();

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <AuthRoot auth={auth} requireAuth={import.meta.env.PROD}>
      {(duties) => <App duties={duties} />}
    </AuthRoot>
  </React.StrictMode>,
);

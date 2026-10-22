import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { loadModules } from "./modules/registry";
import { Shell } from "./shell/Shell";

const modules = loadModules();
const router = createBrowserRouter([
  {
    path: "/",
    element: <Shell modules={modules} />,
    children: modules.flatMap((m) => m.routes),
  },
]);

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);

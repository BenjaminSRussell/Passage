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

const modules = loadModules();
const router = createBrowserRouter([
  {
    path: "/device",
    element: <DeviceHost />,
  },
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
]);

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);

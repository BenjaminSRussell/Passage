import { createElement } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { CopilotHome } from "./pages";
import { ToolCatalogPage } from "./ToolCatalogPage";
import { ToolDetailPage } from "./ToolDetailPage";

function ToolCatalogRoute() {
  const navigate = useNavigate();
  return createElement(ToolCatalogPage, { onOpen: (name: string) => navigate(`/copilot/tools/${encodeURIComponent(name)}`) });
}

function ToolDetailRoute() {
  const { name = "" } = useParams();
  return createElement(ToolDetailPage, { name });
}

const copilot: PassageModule = {
  id: "copilot",
  title: "Copilot",
  description: "Ask questions, explain records and draft text with cited answers, and see the tools the copilot can call for you.",
  routes: [
    { path: "/copilot", element: createElement(CopilotHome) },
    { path: "/copilot/tools", element: createElement(ToolCatalogRoute), handle: { title: "Tool catalog" } },
    { path: "/copilot/tools/:name", element: createElement(ToolDetailRoute), handle: { title: "Tool" } },
  ],
  nav: [
    { label: "Copilot", to: "/copilot", duty: "CopilotUse" },
    { label: "Tool catalog", to: "/copilot/tools", duty: "CopilotUse" },
  ],
};

export default copilot;

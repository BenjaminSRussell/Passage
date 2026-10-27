import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewPositionPage } from "./NewPositionPage";
import { NewWorkerPage } from "./NewWorkerPage";
import { PositionDetailPage } from "./PositionDetailPage";
import { PositionsPage } from "./PositionsPage";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { WorkersPage } from "./WorkersPage";

const hr: PassageModule = {
  id: "hr",
  title: "Human resources",
  description:
    "Workers with masked personal data and an audited reveal, effective-dated employments, positions with FTE and reports-to lines, assignments, and each worker's position and manager on any date.",
  routes: [
    { path: "/hr", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/hr/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/hr/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/hr/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/hr/positions", element: createElement(PositionsPage), handle: { title: "Positions" } },
    { path: "/hr/positions/new", element: createElement(NewPositionPage), handle: { title: "New position" } },
    { path: "/hr/positions/:id", element: createElement(PositionDetailPage), handle: { title: "Position" } },
  ],
  nav: [
    { label: "Workers", to: "/hr/workers", duty: "HcmWorkerInquire" },
    { label: "Positions", to: "/hr/positions", duty: "HcmOrgInquire" },
  ],
};

export default hr;

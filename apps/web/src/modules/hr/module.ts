import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AbsencePlanDetailPage } from "./AbsencePlanDetailPage";
import { AbsencePlansPage } from "./AbsencePlansPage";
import { AbsenceRequestsPage } from "./AbsenceRequestsPage";
import { NewAbsencePlanPage } from "./NewAbsencePlanPage";
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
    "Workers with masked personal data and an audited reveal, effective-dated employments, positions with FTE and reports-to lines, assignments, each worker's position and manager on any date, and absence plans, accrual, balances and requests.",
  routes: [
    { path: "/hr", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/hr/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/hr/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/hr/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/hr/positions", element: createElement(PositionsPage), handle: { title: "Positions" } },
    { path: "/hr/positions/new", element: createElement(NewPositionPage), handle: { title: "New position" } },
    { path: "/hr/positions/:id", element: createElement(PositionDetailPage), handle: { title: "Position" } },
    { path: "/hr/absence/plans", element: createElement(AbsencePlansPage), handle: { title: "Absence plans" } },
    { path: "/hr/absence/plans/new", element: createElement(NewAbsencePlanPage), handle: { title: "New absence plan" } },
    { path: "/hr/absence/plans/:id", element: createElement(AbsencePlanDetailPage), handle: { title: "Absence plan" } },
    { path: "/hr/absence/requests", element: createElement(AbsenceRequestsPage), handle: { title: "Absence requests" } },
  ],
  nav: [
    { label: "Workers", to: "/hr/workers", duty: "HcmWorkerInquire" },
    { label: "Positions", to: "/hr/positions", duty: "HcmOrgInquire" },
    { label: "Absence plans", to: "/hr/absence/plans", duty: "HcmAbsenceAdmin" },
    { label: "Absence requests", to: "/hr/absence/requests", duty: "EssAbsence" },
  ],
};

export default hr;

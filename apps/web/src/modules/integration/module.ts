import { createElement } from "react";
import type { PassageModule } from "../registry";
import { InterchangeDetailPage } from "./InterchangeDetailPage";
import { InterchangesPage } from "./InterchangesPage";
import { MapTesterPage } from "./MapTesterPage";
import { NewPartnerPage } from "./NewPartnerPage";
import { PartnerDetailPage } from "./PartnerDetailPage";
import { PartnersPage } from "./PartnersPage";
import { ReceiveInterchangePage } from "./ReceiveInterchangePage";

const integration: PassageModule = {
  id: "integration",
  title: "Integration",
  description: "EDI trading partners, received interchanges with their 997s, and the map tester.",
  routes: [
    { path: "/integration/partners", element: createElement(PartnersPage), handle: { title: "Trading partners" } },
    { path: "/integration/partners/new", element: createElement(NewPartnerPage), handle: { title: "New trading partner" } },
    { path: "/integration/partners/:id", element: createElement(PartnerDetailPage), handle: { title: "Trading partner" } },
    { path: "/integration/interchanges", element: createElement(InterchangesPage), handle: { title: "Interchanges" } },
    { path: "/integration/interchanges/receive", element: createElement(ReceiveInterchangePage), handle: { title: "Receive interchange" } },
    { path: "/integration/interchanges/:id", element: createElement(InterchangeDetailPage), handle: { title: "Interchange" } },
    { path: "/integration/edi/map-tester", element: createElement(MapTesterPage), handle: { title: "EDI map tester" } },
  ],
  nav: [
    { label: "Trading partners", to: "/integration/partners", duty: "EDI_ADMIN" },
    { label: "Interchanges", to: "/integration/interchanges", duty: "EDI_OPERATE" },
    { label: "EDI map tester", to: "/integration/edi/map-tester", duty: "EDI_ADMIN" },
  ],
};

export default integration;

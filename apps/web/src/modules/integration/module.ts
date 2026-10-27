import { createElement } from "react";
import type { PassageModule } from "../registry";
import { InterchangeDetailPage } from "./InterchangeDetailPage";
import { InterchangesPage } from "./InterchangesPage";
import { MapTesterPage } from "./MapTesterPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { NewPartnerPage } from "./NewPartnerPage";
import { OutboundDocumentPage } from "./OutboundDocumentPage";
import { PartnerDetailPage } from "./PartnerDetailPage";
import { PartnerSlaPage } from "./PartnerSlaPage";
import { PartnersPage } from "./PartnersPage";
import { ReceiveInterchangePage } from "./ReceiveInterchangePage";

const integration: PassageModule = {
  id: "integration",
  title: "Integration",
  description:
    "EDI trading partners, received and sent interchanges with their 997s and masked raw payload, the message monitor with single and bulk reprocess, the partner SLA dashboard, outbound 856/810/855 documents and the map tester.",
  routes: [
    { path: "/integration/partners", element: createElement(PartnersPage), handle: { title: "Trading partners" } },
    { path: "/integration/partners/new", element: createElement(NewPartnerPage), handle: { title: "New trading partner" } },
    { path: "/integration/partners/:id", element: createElement(PartnerDetailPage), handle: { title: "Trading partner" } },
    { path: "/integration/interchanges", element: createElement(InterchangesPage), handle: { title: "Interchanges" } },
    { path: "/integration/interchanges/receive", element: createElement(ReceiveInterchangePage), handle: { title: "Receive interchange" } },
    { path: "/integration/interchanges/outbound", element: createElement(OutboundDocumentPage), handle: { title: "Send EDI document" } },
    { path: "/integration/interchanges/:id", element: createElement(InterchangeDetailPage), handle: { title: "Interchange" } },
    { path: "/integration/messages", element: createElement(MessagesPage), handle: { title: "Message monitor" } },
    { path: "/integration/messages/:id", element: createElement(MessageDetailPage), handle: { title: "Message" } },
    { path: "/integration/partner-sla", element: createElement(PartnerSlaPage), handle: { title: "Partner SLA" } },
    { path: "/integration/edi/map-tester", element: createElement(MapTesterPage), handle: { title: "EDI map tester" } },
  ],
  nav: [
    { label: "Trading partners", to: "/integration/partners", duty: "EDI_ADMIN" },
    { label: "Interchanges", to: "/integration/interchanges", duty: "EDI_OPERATE" },
    { label: "Message monitor", to: "/integration/messages", duty: "EDI_OPERATE" },
    { label: "Partner SLA", to: "/integration/partner-sla", duty: "EDI_OPERATE" },
    { label: "Send EDI document", to: "/integration/interchanges/outbound", duty: "EDI_OPERATE" },
    { label: "EDI map tester", to: "/integration/edi/map-tester", duty: "EDI_ADMIN" },
  ],
};

export default integration;

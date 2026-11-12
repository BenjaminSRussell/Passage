import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CatalogEntryPage } from "./CatalogEntryPage";
import { CatalogPage } from "./CatalogPage";
import { ClientPage } from "./ClientPage";
import { ClientsPage } from "./ClientsPage";
import { EInvoicePage } from "./EInvoicePage";
import { EInvoicesPage } from "./EInvoicesPage";
import { InterchangeDetailPage } from "./InterchangeDetailPage";
import { InterchangesPage } from "./InterchangesPage";
import { MapTesterPage } from "./MapTesterPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { NewCatalogEntryPage } from "./NewCatalogEntryPage";
import { NewClientPage } from "./NewClientPage";
import { NewSyncSubscriptionPage } from "./NewSyncSubscriptionPage";
import { NewWebhookSubscriptionPage } from "./NewWebhookSubscriptionPage";
import { NewPartnerPage } from "./NewPartnerPage";
import { OutboundDocumentPage } from "./OutboundDocumentPage";
import { PartnerDetailPage } from "./PartnerDetailPage";
import { PartnerSlaPage } from "./PartnerSlaPage";
import { PartnersPage } from "./PartnersPage";
import { PeppolInvoicePage } from "./PeppolInvoicePage";
import { PunchoutCheckPage } from "./PunchoutCheckPage";
import { ReceiveInterchangePage } from "./ReceiveInterchangePage";
import { SelfBillingDisputesPage } from "./SelfBillingDisputesPage";
import { SelfBillingPage } from "./SelfBillingPage";
import { SyncDeadLettersPage } from "./SyncDeadLettersPage";
import { SyncDeliveriesPage } from "./SyncDeliveriesPage";
import { SyncSubscriptionPage } from "./SyncSubscriptionPage";
import { SyncSubscriptionsPage } from "./SyncSubscriptionsPage";

const integration: PassageModule = {
  id: "integration",
  title: "Integration",
  description:
    "EDI trading partners, received and sent interchanges with their 997s and masked raw payload, the message monitor with single and bulk reprocess, the partner SLA dashboard, outbound 856/810/855 documents, the map tester, the punchout cart check, self-billing match and Peppol invoice dry runs, the e-invoice list and detail (submit, the network's answer, the Peppol rule check, the Factur-X PDF), the self-billing dispute worklist, and outbound sync: signed webhook subscriptions with a CESQL event filter and signing-secret rotation, REST push subscriptions into the customer's legacy ERP, the delivery monitor and dead letters with replay, API clients with scopes, request quotas and usage, and the API and event catalog with versions, sunset notices and retirement.",
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
    { path: "/integration/punchout", element: createElement(PunchoutCheckPage), handle: { title: "Punchout cart check" } },
    { path: "/integration/self-billing", element: createElement(SelfBillingPage), handle: { title: "Self-billing match" } },
    { path: "/integration/self-billing/disputes", element: createElement(SelfBillingDisputesPage), handle: { title: "Self-billing disputes" } },
    { path: "/integration/peppol", element: createElement(PeppolInvoicePage), handle: { title: "Peppol invoice" } },
    { path: "/integration/einvoices", element: createElement(EInvoicesPage), handle: { title: "E-invoices" } },
    { path: "/integration/einvoices/:id", element: createElement(EInvoicePage), handle: { title: "E-invoice" } },
    { path: "/integration/sync", element: createElement(SyncSubscriptionsPage), handle: { title: "Outbound sync" } },
    { path: "/integration/sync/new", element: createElement(NewSyncSubscriptionPage), handle: { title: "New REST push", duty: "hub.subscription.write" } },
    { path: "/integration/sync/new-webhook", element: createElement(NewWebhookSubscriptionPage), handle: { title: "New webhook", duty: "hub.subscription.write" } },
    { path: "/integration/sync/deliveries", element: createElement(SyncDeliveriesPage), handle: { title: "Sync deliveries" } },
    { path: "/integration/sync/dead-letters", element: createElement(SyncDeadLettersPage), handle: { title: "Sync dead letters" } },
    { path: "/integration/clients", element: createElement(ClientsPage), handle: { title: "API clients" } },
    { path: "/integration/clients/new", element: createElement(NewClientPage), handle: { title: "Register API client" } },
    { path: "/integration/clients/:id", element: createElement(ClientPage), handle: { title: "API client" } },
    { path: "/integration/catalog", element: createElement(CatalogPage), handle: { title: "API catalog" } },
    { path: "/integration/catalog/new", element: createElement(NewCatalogEntryPage), handle: { title: "Publish contract" } },
    { path: "/integration/catalog/:id", element: createElement(CatalogEntryPage), handle: { title: "Catalog entry" } },
    { path: "/integration/sync/:id", element: createElement(SyncSubscriptionPage), handle: { title: "Sync subscription" } },
  ],
  nav: [
    { label: "Trading partners", to: "/integration/partners", duty: "edi.admin" },
    { label: "Interchanges", to: "/integration/interchanges", duty: "edi.operate" },
    { label: "Message monitor", to: "/integration/messages", duty: "edi.operate" },
    { label: "Partner SLA", to: "/integration/partner-sla", duty: "edi.operate" },
    { label: "Send EDI document", to: "/integration/interchanges/outbound", duty: "edi.operate" },
    { label: "EDI map tester", to: "/integration/edi/map-tester", duty: "edi.admin" },
    { label: "Punchout cart check", to: "/integration/punchout", duty: "edi.requisition.create" },
    { label: "Self-billing match", to: "/integration/self-billing", duty: "edi.self-billing.reconcile" },
    { label: "Self-billing disputes", to: "/integration/self-billing/disputes", duty: "edi.self-billing.reconcile" },
    { label: "E-invoices", to: "/integration/einvoices", duty: "edi.einvoice.operate" },
    { label: "Peppol invoice", to: "/integration/peppol", duty: "edi.einvoice.operate" },
    { label: "Outbound sync", to: "/integration/sync", duty: "hub.delivery.read" },
    { label: "Sync deliveries", to: "/integration/sync/deliveries", duty: "hub.delivery.read" },
    { label: "Sync dead letters", to: "/integration/sync/dead-letters", duty: "hub.delivery.read" },
    { label: "API clients", to: "/integration/clients", duty: "hub.delivery.read" },
    { label: "API catalog", to: "/integration/catalog", duty: "hub.delivery.read" },
  ],
};

export default integration;

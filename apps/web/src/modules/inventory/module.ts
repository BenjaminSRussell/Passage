import { createElement } from "react";
import type { PassageModule } from "../registry";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { NewJournalPage } from "./NewJournalPage";
import { OnHandPage } from "./OnHandPage";

const inventory: PassageModule = {
  id: "inventory",
  title: "Inventory",
  description: "On-hand stock by any dimension, and the journals that move it.",
  routes: [
    { path: "/inventory", element: createElement(OnHandPage), handle: { title: "On-hand inquiry" } },
    { path: "/inventory/onhand", element: createElement(OnHandPage) },
    { path: "/inventory/journals", element: createElement(JournalsPage) },
    { path: "/inventory/journals/new", element: createElement(NewJournalPage) },
    { path: "/inventory/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Journal details" } },
  ],
  nav: [
    { label: "On-hand", to: "/inventory/onhand", duty: "inventory.read" },
    { label: "Journals", to: "/inventory/journals", duty: "inventory.read" },
    { label: "New journal", to: "/inventory/journals/new", duty: "inventory.journal.write" },
  ],
};

export default inventory;

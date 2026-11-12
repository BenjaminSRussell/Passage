import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DocumentDesignPage } from "./DocumentDesignPage";

const documentDesign: PassageModule = {
  id: "document-design",
  title: "Document design",
  description: "Document templates, standard text blocks with language versions, and the output rules that route the original and the copies of each business document.",
  routes: [{ path: "/document-design", element: createElement(DocumentDesignPage), handle: { title: "Document design", duty: "print.job.submit" } }],
  nav: [{ label: "Document design", to: "/document-design", duty: "print.job.submit" }],
};

export default documentDesign;

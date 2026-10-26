import { createElement } from "react";
import { useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { MetadataForm } from "./MetadataForm";

function MetadataRoute() {
  const { entity = "Vendor" } = useParams<{ entity: string }>();
  return createElement(MetadataForm, { entity });
}

const metadataModule: PassageModule = {
  id: "metadata",
  title: "Metadata",
  description: "Fields and forms your tenant uses for each record type.",
  routes: [{ path: "/metadata/:entity?", element: createElement(MetadataRoute) }],
  nav: [{ label: "Metadata", to: "/metadata/Vendor", duty: "metadata.read" }],
};

export default metadataModule;

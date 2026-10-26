import { createElement } from "react";
import { useNavigate } from "react-router-dom";
import type { PassageModule } from "../registry";
import { CatalogPage, ProductDetail, ProductHome, ReleasedPage } from "./pages";

function CatalogRoute() {
  const navigate = useNavigate();
  return createElement(CatalogPage, { onOpen: (number: string) => navigate(`/product/${encodeURIComponent(number)}`) });
}

const product: PassageModule = {
  id: "product",
  title: "Product information",
  description: "Products, masters and what's released to each legal entity.",
  routes: [
    { path: "/product", element: createElement(ProductHome) },
    { path: "/product/items", element: createElement(CatalogRoute) },
    { path: "/product/released", element: createElement(ReleasedPage) },
    { path: "/product/:id", element: createElement(ProductDetail), handle: { title: "Product details" } },
  ],
  nav: [
    { label: "Overview", to: "/product", duty: "product.read" },
    { label: "Products", to: "/product/items", duty: "product.read" },
    { label: "Released products", to: "/product/released", duty: "product.released.write" },
  ],
};

export default product;

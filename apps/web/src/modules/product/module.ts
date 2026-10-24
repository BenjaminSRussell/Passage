import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CatalogPage, ProductDetail, ProductHome, ReleasedPage } from "./pages";

const product: PassageModule = {
  id: "product",
  title: "Product information",
  routes: [
    { path: "/product", element: createElement(ProductHome) },
    { path: "/product/items", element: createElement(CatalogPage) },
    { path: "/product/released", element: createElement(ReleasedPage) },
    { path: "/product/:id", element: createElement(ProductDetail) },
  ],
  nav: [
    { label: "PIM workspace", to: "/product", duty: "product.read" },
    { label: "Products", to: "/product/items", duty: "product.read" },
    { label: "Released products", to: "/product/released", duty: "product.released.write" },
  ],
};

export default product;

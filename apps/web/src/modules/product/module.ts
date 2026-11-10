import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CatalogPage, ProductDetail, ProductHome, ReleasedPage } from "./pages";
import { ItemGroupsPage } from "./ItemGroupsPage";
import { EditProductPage, NewProductPage } from "./ProductForms";

const product: PassageModule = {
  id: "product",
  title: "Product information",
  description: "Products, masters and what's released to each legal entity.",
  routes: [
    { path: "/product", element: createElement(ProductHome) },
    { path: "/product/items", element: createElement(CatalogPage) },
    { path: "/product/items/new", element: createElement(NewProductPage), handle: { title: "New product", duty: "product.write" } },
    { path: "/product/released", element: createElement(ReleasedPage) },
    { path: "/product/setup/item-groups", element: createElement(ItemGroupsPage), handle: { title: "Item groups" } },
    { path: "/product/:id", element: createElement(ProductDetail), handle: { title: "Product details" } },
    { path: "/product/:id/edit", element: createElement(EditProductPage), handle: { title: "Edit product", duty: "product.write" } },
  ],
  nav: [
    { label: "Overview", to: "/product", duty: "product.read" },
    { label: "Products", to: "/product/items", duty: "product.read" },
  ],
};

export default product;

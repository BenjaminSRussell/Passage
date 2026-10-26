import { createElement } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { CatalogPage, ProductDetail, ProductHome, ProductNotFound, ReleasedPage, sampleProducts } from "./pages";

function CatalogRoute() {
  const navigate = useNavigate();
  return createElement(CatalogPage, { onOpen: (number: string) => navigate(`/product/${encodeURIComponent(number)}`) });
}

function ProductDetailRoute() {
  const { id } = useParams();
  const navigate = useNavigate();
  const found = sampleProducts.find((p) => p.number === id);
  return found
    ? createElement(ProductDetail, { product: found })
    : createElement(ProductNotFound, { onBack: () => navigate("/product/items") });
}

const product: PassageModule = {
  id: "product",
  title: "Product information",
  description: "Products, masters and what's released to each legal entity.",
  routes: [
    { path: "/product", element: createElement(ProductHome) },
    { path: "/product/items", element: createElement(CatalogRoute) },
    { path: "/product/released", element: createElement(ReleasedPage) },
    { path: "/product/:id", element: createElement(ProductDetailRoute), handle: { title: "Product details" } },
  ],
  nav: [
    { label: "Overview", to: "/product", duty: "product.read" },
    { label: "Products", to: "/product/items", duty: "product.read" },
    { label: "Released products", to: "/product/released", duty: "product.released.write" },
  ],
};

export default product;

export function ProductHome({
  counts = { products: 500, masters: 50, released: 500, incomplete: 0 },
}: {
  counts?: { products: number; masters: number; released: number; incomplete: number };
}) {
  return (
    <section>
      <h2>Product information management</h2>
      <ul aria-label="PIM tiles">
        <li>Products {counts.products}</li>
        <li>Masters {counts.masters}</li>
        <li>Released {counts.released}</li>
        <li>Incomplete attributes {counts.incomplete}</li>
      </ul>
    </section>
  );
}

export function CatalogPage({
  items = [
    { number: "SHIRT", name: "Knit shirt", subtype: "master", state: "active" },
    { number: "BOLT", name: "Hex bolt", subtype: "product", state: "active" },
  ],
}: {
  items?: { number: string; name: string; subtype: string; state: string }[];
}) {
  return (
    <section>
      <h2>All products and masters</h2>
      <table aria-label="Products">
        <thead>
          <tr>
            <th scope="col">Number</th>
            <th scope="col">Name</th>
            <th scope="col">Subtype</th>
            <th scope="col">State</th>
          </tr>
        </thead>
        <tbody>
          {items.map((row) => (
            <tr key={row.number}>
              <td>{row.number}</td>
              <td>{row.name}</td>
              <td>{row.subtype}</td>
              <td>{row.state}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export function ReleasedPage() {
  return (
    <section>
      <h2>Released products</h2>
      <p>Item group, model group, default site and warehouse per legal entity.</p>
    </section>
  );
}

export function ProductDetail({
  product = { number: "SHIRT", name: "Knit shirt", gtin: "06141411234579" },
}: {
  product?: { number: string; name: string; gtin: string };
}) {
  return (
    <section>
      <h2>{product.name}</h2>
      <p>Number {product.number}</p>
      <p>GTIN {product.gtin}</p>
    </section>
  );
}

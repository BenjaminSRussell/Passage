# product web module

Product information management workspace for wave 8. Discovered by the shell via `module.ts`.

Reads and writes the product service (`/api/v1/product`): catalog search with keyset paging, product detail, create and edit.
The create and edit forms set the item group and the batch, serial number and catch weight flags under "More options" (item groups come from `GET /api/v1/product/setup/item-groups`; a service can't be tracked), and the detail page shows them.
Picking a product through the shared lookup on a purchase order or sales order line fills the line's unit from the product's base unit (`LookupOption.unit`); the person can still change it.
Still to come: variants, item group and tracking flags on released products, pricing and bulk import.

Needs: none beyond the shell lockfile.

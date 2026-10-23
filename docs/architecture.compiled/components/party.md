# Party

## Purpose & Boundaries
Context `party` (database `passage_party`; wave 7), TS only (`party-ts`). One row per real-world organization or person with layered roles (customer, vendor, contact, employee, bank, prospect). Owns party, customer/vendor roles, addresses, tax registrations, groups, payment/delivery terms, credit limits, dedup/merge, governance. Party and contacts/bank details are owned here (portal profile endpoints moved from sales/procurement).

## Interfaces
Generated CRUD for parties, customers, vendors, contacts, addresses, tax registrations, groups, payment terms, delivery terms. Custom:
- `POST /v1/party/parties:match` (duplicate blocking, precision >= 0.95; never creates), `GET /duplicate-candidates`, `POST /duplicate-candidates/{id}:dismiss`.
- `POST /v1/party/parties/{id}:merge` (governed, approval workflow, survivorship; emits `party.merged.v1`; privilege `party.merge`).
- `GET /parties/{id}/roles`; `POST /customers`; `PATCH /customers/{id}/credit-limit` (sensitive: `403 approval_required` + task; privilege `customer.creditlimit`); `POST /vendors`.
- Bank accounts: `POST|GET /vendors/{id}/bank-accounts` (P3, encrypted `*_secure`, masked `****1234`, approval workflow, SoD with payment runs), `POST .../{bankId}:reveal` (audited, `vendorbank.reveal`).
- `POST /tax-registrations/{id}:validate` (VIES/regional), `POST /addresses:validate`.
- Portal: `GET|PATCH /v1/party/portal/profile` (customer scope; bank-detail changes route to bank-change approval).
- `POST /change-requests`, `GET /change-requests/{id}` (governance for sensitive fields).

## Data Ownership
EN-013..017: `party.party` (party_number via sequence PTY `PTY-{######}`, party_type organization|person, status draft|pending_approval|active|blocked|inactive|merged, name_normalized, phonetic_key, merged_into_party_id, search_vector), `party_role` (UQ (tenant_id, party_id, role_type)), `customer` (ML; customer_account UQ per tenant+LE; credit_limit numeric(20,6); credit_check_mode none|warn|hold; hold_status none|invoice|delivery|all; posting_profile_id REF ledger), `vendor` (ML; vendor_account; payment_method check|ach|sepa|wire|card|other; is_1099), address, party_address, contact, tax_registration (registration_number_hash HMAC), party_secure and party_bank_account_secure (P3, CDC-excluded), party_bank_account, customer_group/vendor_group, payment_terms, delivery_terms, credit_limit_change (I), party_merge (I), party_match_candidate (CHECK party_a_id < party_b_id). Indexes IX-PA-01..27 (trgm GIN on name_normalized, phonetic+country, tax hash).

## Events Produced
`party.party.created.v1` (partyId, partyNumber, partyType, displayName, roles[], countryCode, internalLegalEntityId? drives intercompany), `party.party.merged.v1` (mergeId, survivorPartyId, mergedPartyIds[], roleIdMap[], mergedAt; key T:survivorPartyId), `party.customer.creditchanged.v1` (customerId, partyId, legalEntityId, previousLimit, newLimit, creditStatus ok|hold|blocked, reason, effectiveFrom). Snapshot topics for party.party and party.customer. Also approval via `/passage/party/credit`.

## Events Consumed
`party-approvals` (workflowtask.completed), `party-legalentity-ref` (legalentity.created), `party-numbersequence` (platform.numbersequence.changed). Other contexts consume `party.merged` (`<ctx>-partymerge`) to rewrite their `party_id` columns.

## Dependencies
internal: platform (legal entities, workflow), ledger (posting_profile REF), edge-identity. external: VIES and national tax-id validators (validation deferred and record saved `unverified` on failure).

## Gotchas & Constraints
- Dedup: candidate generation via trigram >= 0.6, phonetic+country, same tax-id hash; Fellegi-Sunter scoring; merged parties keep `merged_into_party_id`; fiscal rows keep id plus pointer, non-fiscal rows rewritten by each context on `party.merged`.
- P3 columns never in events/CDC/logs; person-party national ids only in party_secure.
- `registration_number` NULL for person parties.
- Merge/new bank accounts require approval workflow and SoD vs payment runs.
- Bank-change from portals routes to approval, never direct write.
- Customer/vendor are per legal entity; party is tenant-level.

## Source Sections
- Backend 3.2 party row (287), 5.6 (572-594); Data EN-013..017 (2127-2228), 5.2 ERD (1637-1660); Events 3.3 (3998-4000), consumer rows 6,7,8 (4189-4191); AX/SAP party mapping (3547-3552, 3589-3592)

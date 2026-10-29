# localization (PSG-48, wave 48, slice 1)

Country localization for Passage. Packs are metadata, never code forks (FR-48.1). Slice 1 loads the
seven first country packs from `countrypacks/` at start-up, validates them, and exposes:

- the pack catalogue and per-pack detail;
- install checks (country match, one primary pack per legal entity, fiscal calendar, pack minimum retention);
- tax-id validation for the pack schemes (GB VAT, DE USt-IdNr, FR TVA, IN GSTIN with check character, MX RFC, CA BN, US EIN);
- the UK MTD VAT return check against HMRC's VAT (MTD) API 1.0 submit-return body (`periodKey`,
  `vatDueSales` … `totalAcquisitionsExVAT`, `finalised`): every box present within HMRC's range and scale
  (boxes 1 to 4 signed with pence, box 5 non-negative, boxes 6 to 9 signed whole pounds), `finalised` true,
  box 3 = box 1 + box 2 and box 5 = |box 3 - box 4|. Amounts are decimals, never floats.

The compliance workspace (`apps/web/src/modules/compliance`, routes `/compliance/packs` and
`/compliance/packs/:code`) lists the packs, shows a pack's contents and runs the install and tax-id checks.

Installs per legal entity, pack versions and upgrade preview need the store (later slice). The service is
stateless in this slice and needs no database.

## Run and test

```
COUNTRY_PACKS_DIR=../../countrypacks go run ./cmd/localization
go vet ./... && go test ./...
```

The tests read `countrypacks/` directly; they do not need a database. The service refuses to start when
any pack fails validation, or when the directory does not hold the seven packs.

## Deploy

`charts/localization` (`helm lint` clean): Deployment, Service, optional ServiceMonitor; no database. The
`Dockerfile` builds from the repository root and copies `countrypacks/` into the image at `/countrypacks`.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `COUNTRY_PACKS_DIR` | `countrypacks` | directory of `<code>/pack.json` |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | listeners |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty `LocalizationAdmin` |
| `COM-0002` | 400 | request failed validation |
| `LOC-0501` | 422 | fiscal calendar has open periods incompatible with the pack |
| `LOC-0502` | 422 | retention below the statutory pack minimum |
| `LOC-0503` | 422 | pack country does not match the legal entity |
| `LOC-0504` | 422 | legal entity already has a primary pack |
| `LOC-0505` | 422 | invalid pack metadata or tax id |
| `LOC-0506` | 404 | no pack with that code |
| `ERP-0801` | 422 | statutory output fails its schema (`errors` lists fields) |

## Wave reference

PSG-48 slice 1 covers FR-48.1 (framework and validation), FR-48.2 (seven packs, metadata only) and the
UK part of AC1. See `NOT-IMPLEMENTED.md`.

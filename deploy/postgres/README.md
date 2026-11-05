# outbox-postgres

Postgres for a service whose `outbox` Debezium relays. Release name is the service (`metadata`, `batch`).

Credentials come from the Secret `<release>-postgres` (ExternalSecret, Vault `<prefix>/<release>-postgres/{postgres-password,app-password,cdc-password}`). They are mounted as files, not env. Each database has its own CDC password; the matching connector reads its copy from its own Secret in `kafka` (`debezium-cdc-<release>`, `deploy/envs/<env>/kafka-secrets`).

initdb runs only on an empty data volume. Rotating a password later: update Vault for both the database Secret and the `kafka` Secret, then `ALTER ROLE debezium_cdc PASSWORD '...'` (and `passage_app`) on the running database, then restart the connector.

`cdc.enabled: false` makes a plain database for a component with no outbox (release `apicurio` in `kafka`, Apicurio Registry's SQL storage): no logical WAL, no `debezium_cdc` role or `cdc-password` key, and ingress only from `clientApps` (required then). The registry reads its own Secret `apicurio-database` (`deploy/envs/<env>/kafka-secrets/apicurio-database.yaml`) built from the same `app-password` key.

Service migrations `metadata/0006` and `batch/0011` grant SELECT on `outbox` to `debezium_cdc` only if the role exists when they run. If a service migrated before this database existed, delete that row from `schema_migrations` and restart (or run the GRANT by hand).

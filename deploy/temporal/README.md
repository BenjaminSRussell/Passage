# Temporal development deployment

The values configure the official Temporal Helm chart for PostgreSQL-backed
history and visibility, with the architecture-fixed 512 history shards.

```sh
helm repo add temporal https://go.temporal.io/helm-charts
helm upgrade --install temporal temporal/temporal \
  --namespace temporal --create-namespace \
  --version 0.74.0 --values deploy/temporal/values.yaml
```

The Argo CD Applications for this chart (`0.74.0`, the last release that accepts these values) and its database live in `deploy/envs/dev/apps/temporal.yaml`. `temporal-postgres` (chart `deploy/temporal-postgres`, wave -1) serves `temporal-postgres-rw.temporal.svc` and an ExternalSecret writes the `temporal-database` secret (key `password`) from `passage/dev/temporal-postgres/password` in the `passage` ClusterSecretStore; Temporal's schema job creates `temporal_visibility`. Strimzi, Kafka, Apicurio Registry and Debezium Connect are wired in `deploy/envs/dev/apps/event-backbone.yaml`.

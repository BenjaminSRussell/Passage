# Temporal development deployment

The values configure the official Temporal Helm chart for PostgreSQL-backed
history and visibility, with the architecture-fixed 512 history shards.

```sh
helm repo add temporal https://go.temporal.io/helm-charts
helm upgrade --install temporal temporal/temporal \
  --namespace temporal --create-namespace \
  --version 0.74.0 --values deploy/temporal/values.yaml
```

Argo CD installs the same chart (`0.74.0`, the last release that accepts these
values) and values file through `deploy/envs/dev/apps/event-backbone.yaml`,
which also deploys the Strimzi operator, the Kafka cluster, Apicurio Registry
and Kafka Connect/Debezium from `deploy/kafka` in sync-wave order.

The `temporal-database` secret and CloudNativePG clusters are environment
handoffs because `deploy/envs/dev/apps` is owned by Wave 01.

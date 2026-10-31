# Temporal development deployment

The values configure the official Temporal Helm chart for PostgreSQL-backed
history and visibility, with the architecture-fixed 512 history shards.

```sh
helm repo add temporal https://go.temporal.io/helm-charts
helm upgrade --install temporal temporal/temporal \
  --namespace temporal --create-namespace \
  --version 0.74.0 --values deploy/temporal/values.yaml
```

`application.yaml` is the Argo CD Application for the same chart (`0.74.0`, the last release that accepts these values). It is not in `deploy/envs/dev/apps` on purpose: root-dev would sync it before its database exists. Strimzi, Kafka, Apicurio Registry and Debezium Connect are wired in `deploy/envs/dev/apps/event-backbone.yaml`.

Before moving `application.yaml` into `deploy/envs/dev/apps`, provision the `temporal-database` secret and the CloudNativePG cluster `temporal-postgres` (service `temporal-postgres-rw.temporal.svc`, databases `temporal` and `temporal_visibility`) in namespace `temporal`; neither exists in any dev app yet.

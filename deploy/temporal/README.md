# Temporal development deployment

The values configure the official Temporal Helm chart for PostgreSQL-backed
history and visibility, with the architecture-fixed 512 history shards.

```sh
helm repo add temporal https://go.temporal.io/helm-charts
helm upgrade --install temporal temporal/temporal \
  --namespace temporal --create-namespace \
  --values deploy/temporal/values.yaml
```

The `temporal-database` secret and CloudNativePG clusters are environment
handoffs because `deploy/envs/dev/apps` is owned by Wave 01.

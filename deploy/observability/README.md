# Observability (wave 5)

Helm chart `charts/observability` ships Grafana SLO dashboards, Tempo/Loki/Prometheus config and PagerDuty-compatible Alertmanager routes. It does not vendor kube-prometheus-stack; wave 1 owns cluster add-ons.

A UI click emits W3C `traceparent` from `apps/web/src/shell/telemetry.ts`. The documented hop is web-shell → gateway → service → postgres and kafka consumer, which the `passage-slo` Tempo panel queries.

```
helm lint deploy/observability/charts/observability
```

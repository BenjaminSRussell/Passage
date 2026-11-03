# passage-cloudevents-enricher

Kafka Connect single message transform that completes the CloudEvents 1.0 structured envelope of
Debezium outbox records (architecture section 2.5).

`platform/go/outbox` stores the whole event JSON (`platform/go/events.Event`) in `outbox.payload` and the
tenant in `outbox.tenant_id`. The Debezium `EventRouter` (with `table.expand.json.payload=true`) routes it to
`passage.<context>.<entity>`, hands on the payload as a `Struct` whose schema it inferred from the JSON, and passes
`tenant_id` on as the `tenantid` record header. This SMT runs after the router (`transforms: outbox,enrich`) and:

- sets `specversion=1.0` and `datacontenttype=application/json`; sets `source` (default `passage`) and `dataschema`
  (from a template, off by default) only when the event has none
- fills `tenantid` from the router header (the Go envelope always writes `tenantid`, possibly empty) and
  `legalentityid` from the payload's top-level `legalEntityId` / `legal_entity_id`, as `events.New` does
- flattens an optional `headers` object in the event into string extension attributes (`correlationid`,
  `causationid`, `traceparent`, `actor`, ...); names must be 1-20 chars of `a-z0-9`, core attributes are never
  overwritten, nested values are skipped. The outbox `headers` jsonb column is not routed into the value (the Go
  outbox leaves it `{}`), so today this only applies to producers that put `headers` in the event itself
- sets the Kafka `content-type` header to `application/cloudevents+json` (CloudEvents Kafka binding, structured mode)
  and copies the attributes to `ce_*` string headers (`ce_type`, `ce_id`, `ce_source`, `ce_subject`, `ce_time`,
  `ce_tenantid`, `ce_legalentityid`, ...) so consumers can drop unhandled types without parsing the value
- rejects (DataException) records with a missing id or time, a type that does not match
  `dev.passage.<context>.<entity>.<verb>.v<n>`, a specversion other than 1.0, or no tenant. The connectors keep the
  default `errors.tolerance=none`, so such a record stops the task rather than publishing a broken event
- never overwrites an attribute the producer set, is idempotent, keeps a `Struct` value a `Struct` (new attributes
  are optional strings) and also accepts schemaless `Map` values; tombstones and other values pass through

Config (`transforms.enrich.*`): `source.default`, `dataschema.template` (`{type}` substituted), `headers.field`
(default `headers`, empty disables), `tenant.header` (default `tenantid`).

## Build and test

```
export JAVA_HOME=/opt/homebrew/opt/openjdk@21 PATH=$JAVA_HOME/bin:$PATH
mvn -q verify        # Java 17 target, Kafka 4.0.0 (matches deploy/kafka/connect.yaml), JUnit 5
```

`CloudEventsEnricherTest` covers the transform on its own; `EventRouterChainTest` feeds a Debezium outbox insert
through the real `EventRouter` (debezium-core 3.0.7.Final, test scope) with the `connect.yaml` settings, then the
enricher, `JsonConverter` and the default `SimpleHeaderConverter`, and asserts on the bytes consumers read.
CI runs both in the `enricher` job of `.github/workflows/ci.yml`.

The jar `target/passage-cloudevents-enricher-0.1.0.jar` has no runtime dependencies (Kafka classes are `provided`)
and lists the SMT in `META-INF/services` for Connect's `service_load` plugin discovery.

## Deployment

`deploy/kafka-connect/Dockerfile` builds the Connect image: Strimzi 0.46.0 / Kafka 4.0.0, the Debezium Postgres
connector plugin (checksum-verified) and this jar under `/opt/kafka/plugins`. The `connect-image` CI job builds it
on pull requests and, on `main`, pushes `ghcr.io/benjaminsrussell/passage-connect:<tag from connect.yaml>` plus the
commit sha and signs it. `deploy/kafka/connect.yaml` runs that image (`spec.image`, no Strimzi in-cluster build) and
every outbox connector chains it:

```yaml
transforms: outbox,enrich
transforms.enrich.type: dev.passage.connect.cloudevents.CloudEventsEnricher
transforms.enrich.source.default: passage
```

Bumping the enricher or Debezium version means changing the `Dockerfile`, the image tag in `connect.yaml`, and (for
Debezium) `debezium.version` in `pom.xml`.

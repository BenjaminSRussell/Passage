# passage-cloudevents-enricher

Kafka Connect single message transform that completes the CloudEvents 1.0 structured envelope of
Debezium outbox records (architecture section 2.5).

`platform/go/outbox` stores the whole event JSON (`platform/go/events.Event`) in `outbox.payload` and the
tenant in `outbox.tenant_id`. The Debezium `EventRouter` (with `table.expand.json.payload=true`) routes it to
`passage.<context>.<entity>` and passes `tenant_id` on as the `tenantid` record header. This SMT runs after
the router and:

- sets `specversion=1.0` and `datacontenttype=application/json`; sets `source` (default `passage`) and `dataschema`
  (from a template, off by default) only when the event has none
- fills `tenantid` from the router header and `legalentityid` from the payload's top-level `legalEntityId` / `legal_entity_id`
- flattens an optional `headers` map into extension attributes (`correlationid`, `causationid`, `traceparent`, `actor`, ...);
  names must be 1-20 chars of `a-z0-9`, core attributes are never overwritten, nested values are skipped
- copies the attributes to `ce_*` Kafka record headers (`ce_type`, `ce_id`, `ce_source`, `ce_subject`, `ce_time`,
  `ce_tenantid`, `ce_legalentityid`, ...) so consumers can drop unhandled types without parsing the value
- rejects (DataException, so `errors.tolerance` applies) records with a missing id or time, a type that does not match
  `dev.passage.<context>.<entity>.<verb>.v<n>`, a specversion other than 1.0, or no tenant
- never overwrites an attribute the producer set, is idempotent, and passes tombstones and non-map values through

Config (`transforms.enrich.*`): `source.default`, `dataschema.template` (`{type}` substituted), `headers.field`
(default `headers`, empty disables), `tenant.header` (default `tenantid`).

## Build and test

```
export JAVA_HOME=/opt/homebrew/opt/openjdk@21 PATH=$JAVA_HOME/bin:$PATH
mvn -q verify        # Java 17 target, Kafka 4.0.0 (matches deploy/kafka/connect.yaml), JUnit 5
```

The jar `target/passage-cloudevents-enricher-0.1.0.jar` has no runtime dependencies (Kafka classes are `provided`).

## Wiring into a connector

Not yet wired into `deploy/kafka/connect.yaml`: the Strimzi build needs a published jar, and a URL that does not
resolve would break the Connect image build. After publishing the jar (for example as a release asset), add it to
`KafkaConnect.spec.build.plugins`:

```yaml
- name: passage-cloudevents-enricher
  artifacts:
    - type: jar
      url: <published jar url>
      sha512sum: <sha512 of the jar>
```

and chain it after the router in each outbox `KafkaConnector`:

```yaml
transforms: outbox,enrich
transforms.enrich.type: dev.passage.connect.cloudevents.CloudEventsEnricher
transforms.enrich.source.default: passage
```

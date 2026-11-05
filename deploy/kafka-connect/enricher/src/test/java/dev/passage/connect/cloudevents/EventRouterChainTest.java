package dev.passage.connect.cloudevents;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.ServiceLoader;

import org.apache.kafka.connect.data.Schema;
import org.apache.kafka.connect.data.SchemaBuilder;
import org.apache.kafka.connect.data.Struct;
import org.apache.kafka.connect.errors.DataException;
import org.apache.kafka.connect.header.Header;
import org.apache.kafka.connect.json.JsonConverter;
import org.apache.kafka.connect.source.SourceRecord;
import org.apache.kafka.connect.storage.SimpleHeaderConverter;
import org.apache.kafka.connect.transforms.Transformation;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import io.debezium.data.Envelope;
import io.debezium.data.Json;
import io.debezium.data.Uuid;
import io.debezium.transforms.outbox.EventRouter;

/**
 * Runs Debezium's EventRouter with the deploy/kafka/connect.yaml settings, then the enricher, then the
 * connectors' JsonConverter and the default header converter: the bytes a Passage consumer reads.
 */
class EventRouterChainTest {

    static final String TENANT = "0190a1b2-0000-7000-8000-000000000001";
    static final String LE = "0190a1b2-0000-7000-8000-0000000000aa";
    static final ObjectMapper JSON = new ObjectMapper();

    static final Schema ROW = SchemaBuilder.struct().name("passage.receiving.public.outbox.Value")
            .field("id", Schema.STRING_SCHEMA)
            .field("tenant_id", Uuid.builder().build())
            .field("aggregate_type", Schema.STRING_SCHEMA)
            .field("aggregate_id", Schema.STRING_SCHEMA)
            .field("type", Schema.STRING_SCHEMA)
            .field("payload", Json.builder().build())
            .field("headers", Json.builder().build())
            .build();
    static final Schema SOURCE = SchemaBuilder.struct().name("io.debezium.connector.postgresql.Source")
            .field("db", Schema.STRING_SCHEMA).build();
    static final Envelope ENVELOPE = Envelope.defineSchema().withName("passage.receiving.public.outbox.Envelope")
            .withRecord(ROW).withSource(SOURCE).build();

    EventRouter<SourceRecord> router;
    CloudEventsEnricher<SourceRecord> enricher;
    JsonConverter values;
    SimpleHeaderConverter headers;

    @BeforeEach
    void setUp() {
        router = new EventRouter<>();
        Map<String, String> cfg = new HashMap<>();
        cfg.put("table.field.event.id", "id");
        cfg.put("table.field.event.key", "aggregate_id");
        cfg.put("table.field.event.payload", "payload");
        cfg.put("route.by.field", "aggregate_type");
        cfg.put("route.topic.replacement", "passage.receiving.${routedByValue}");
        cfg.put("table.expand.json.payload", "true");
        cfg.put("table.fields.additional.placement", "tenant_id:header:tenantid");
        router.configure(cfg);
        enricher = new CloudEventsEnricher<>();
        enricher.configure(Map.of(
                "source.default", "passage",
                "dataschema.template", "https://schemas.passage.dev/{type}.json"));
        values = new JsonConverter();
        values.configure(Map.of("schemas.enable", "false"), false);
        headers = new SimpleHeaderConverter();
        headers.configure(Map.of());
    }

    @AfterEach
    void tearDown() {
        router.close();
        enricher.close();
    }

    /** The payload platform/go/outbox.Enqueue writes: json.Marshal(events.Event). */
    static String goEvent(String data) {
        return "{\"specversion\":\"1.0\",\"id\":\"01JB0000000000000000000001\",\"source\":\"passage/receiving\","
                + "\"type\":\"dev.passage.receiving.receipt.posted.v1\",\"subject\":\"receipt/R-1\","
                + "\"time\":\"2026-11-03T10:00:00.123456Z\",\"datacontenttype\":\"application/json\","
                + "\"data\":" + data + ",\"tenantid\":\"\"}";
    }

    static SourceRecord outboxInsert(String payload) {
        Struct row = new Struct(ROW).put("id", "01JB0000000000000000000001").put("tenant_id", TENANT)
                .put("aggregate_type", "receipt").put("aggregate_id", "R-1")
                .put("type", "dev.passage.receiving.receipt.posted.v1").put("payload", payload).put("headers", "{}");
        Struct value = ENVELOPE.create(row, new Struct(SOURCE).put("db", "passage_receiving"), Instant.now());
        return new SourceRecord(Map.of(), Map.of(), "passage.receiving.public.outbox", null,
                Schema.STRING_SCHEMA, "R-1", ENVELOPE.schema(), value);
    }

    SourceRecord chain(SourceRecord in) {
        return enricher.apply(router.apply(in));
    }

    String wireHeader(SourceRecord r, String name) {
        Header h = r.headers().lastWithName(name);
        if (h == null) {
            return null;
        }
        byte[] b = headers.fromConnectHeader(r.topic(), name, h.schema(), h.value());
        return new String(b, StandardCharsets.UTF_8);
    }

    JsonNode wireValue(SourceRecord r) throws Exception {
        return JSON.readTree(values.fromConnectData(r.topic(), r.valueSchema(), r.value()));
    }

    @Test
    void routerHandsTheEnricherAStruct() {
        SourceRecord routed = router.apply(outboxInsert(goEvent("{\"legalEntityId\":\"" + LE + "\"}")));
        assertInstanceOf(Struct.class, routed.value(), "table.expand.json.payload yields a Struct, not a Map");
    }

    @Test
    void goOutboxEventBecomesCompleteCloudEvent() throws Exception {
        SourceRecord out = chain(outboxInsert(goEvent("{\"legalEntityId\":\"" + LE + "\",\"lines\":[{\"qty\":\"2\"}]}")));
        assertEquals("passage.receiving.receipt", out.topic());
        assertEquals("R-1", out.key());

        JsonNode v = wireValue(out);
        assertEquals("1.0", v.get("specversion").asText());
        assertEquals("01JB0000000000000000000001", v.get("id").asText());
        assertEquals("dev.passage.receiving.receipt.posted.v1", v.get("type").asText());
        assertEquals("2026-11-03T10:00:00.123456Z", v.get("time").asText());
        assertEquals(TENANT, v.get("tenantid").asText(), "empty tenantid in the payload is filled from outbox.tenant_id");
        assertEquals(LE, v.get("legalentityid").asText());
        assertEquals("2", v.get("data").get("lines").get(0).get("qty").asText(), "data passes through");
        assertEquals("https://schemas.passage.dev/dev.passage.receiving.receipt.posted.v1.json",
                v.get("dataschema").asText());

        assertEquals("application/cloudevents+json", wireHeader(out, "content-type"));
        assertEquals("dev.passage.receiving.receipt.posted.v1", wireHeader(out, "ce_type"), "raw UTF-8, not base64");
        assertEquals("01JB0000000000000000000001", wireHeader(out, "ce_id"));
        assertEquals(TENANT, wireHeader(out, "ce_tenantid"));
        assertEquals(LE, wireHeader(out, "ce_legalentityid"));
        assertEquals("receipt/R-1", wireHeader(out, "ce_subject"));
        assertEquals(TENANT, wireHeader(out, "tenantid"), "router header is kept");
    }

    @Test
    void producerAttributesAndHeadersObjectInTheStruct() throws Exception {
        String payload = goEvent("{\"legal_entity_id\":\"" + LE + "\"}")
                .replace("\"tenantid\":\"\"", "\"tenantid\":\"" + TENANT + "\",\"legalentityid\":\"le-producer\","
                        + "\"headers\":{\"traceparent\":\"00-4bf9-00f0-01\",\"correlationid\":\"req-9\",\"type\":\"evil\",\"n\":3}");
        JsonNode v = wireValue(chain(outboxInsert(payload)));
        assertEquals("le-producer", v.get("legalentityid").asText(), "producer value wins");
        assertEquals("00-4bf9-00f0-01", v.get("traceparent").asText());
        assertEquals("req-9", v.get("correlationid").asText());
        assertEquals("3", v.get("n").asText());
        assertEquals("dev.passage.receiving.receipt.posted.v1", v.get("type").asText());
        assertFalse(v.has("headers"));
    }

    @Test
    void schemaIsReusedForTheSameShape() {
        SourceRecord a = chain(outboxInsert(goEvent("{\"legalEntityId\":\"" + LE + "\"}")));
        SourceRecord b = chain(outboxInsert(goEvent("{\"legalEntityId\":\"" + LE + "\"}")));
        assertTrue(a.valueSchema() == b.valueSchema());
        assertTrue(a.valueSchema().field("tenantid") != null);
    }

    @Test
    void nonConformingOutboxEventIsRejected() {
        String bad = goEvent("{}").replace("dev.passage.receiving.receipt.posted.v1", "receipt.posted");
        assertThrows(DataException.class, () -> chain(outboxInsert(bad)));
    }

    /**
     * The route of outbox-sales and outbox-fulfillment in deploy/kafka/connect.yaml (keep in step): by the event type's
     * entity, not aggregate_type, because those services write several entities under one aggregate_type.
     */
    static EventRouter<SourceRecord> typeRouter(String context) {
        EventRouter<SourceRecord> r = new EventRouter<>();
        Map<String, String> cfg = new HashMap<>();
        cfg.put("table.field.event.id", "id");
        cfg.put("table.field.event.key", "aggregate_id");
        cfg.put("table.field.event.payload", "payload");
        cfg.put("route.by.field", "type");
        cfg.put("route.topic.regex",
                "dev\\.passage\\." + context + "\\.(?<routedByValue>[a-z][a-z0-9]*)\\.[a-z][a-z0-9]*\\.v[0-9]+");
        cfg.put("route.topic.replacement", "passage." + context + ".${routedByValue}");
        cfg.put("table.expand.json.payload", "true");
        cfg.put("table.fields.additional.placement", "tenant_id:header:tenantid");
        r.configure(cfg);
        return r;
    }

    static SourceRecord outboxInsert(String context, String aggregateType, String type) {
        String payload = goEvent("{\"legalEntityId\":\"" + LE + "\"}")
                .replace("dev.passage.receiving.receipt.posted.v1", type)
                .replace("passage/receiving", "passage/" + context);
        Struct row = new Struct(ROW).put("id", "01JB0000000000000000000001").put("tenant_id", TENANT)
                .put("aggregate_type", aggregateType).put("aggregate_id", "A-1")
                .put("type", type).put("payload", payload).put("headers", "{}");
        Struct value = ENVELOPE.create(row, new Struct(SOURCE).put("db", "passage_" + context), Instant.now());
        return new SourceRecord(Map.of(), Map.of(), "passage." + context + ".public.outbox", null,
                Schema.STRING_SCHEMA, "A-1", ENVELOPE.schema(), value);
    }

    @Test
    void salesAndFulfillmentRouteByTheTypesEntity() throws Exception {
        String[][] cases = {
            // context, aggregate_type written by the service, event type, topic (AsyncAPI channel)
            {"sales", "order", "dev.passage.sales.orderprogress.recorded.v1", "passage.sales.orderprogress"},
            {"sales", "order", "dev.passage.sales.order.confirmed.v1", "passage.sales.order"},
            {"sales", "order", "dev.passage.sales.orderhold.applied.v1", "passage.sales.orderhold"},
            {"sales", "allocation", "dev.passage.sales.allocation.consumed.v1", "passage.sales.allocation"},
            {"fulfillment", "shipment", "dev.passage.fulfillment.packingslip.cancelled.v1", "passage.fulfillment.packingslip"},
            {"fulfillment", "shipment", "dev.passage.fulfillment.packingslip.posted.v1", "passage.fulfillment.packingslip"},
            {"fulfillment", "drop_ship", "dev.passage.fulfillment.dropship.created.v1", "passage.fulfillment.dropship"},
        };
        for (String[] c : cases) {
            EventRouter<SourceRecord> r = typeRouter(c[0]);
            try {
                SourceRecord out = enricher.apply(r.apply(outboxInsert(c[0], c[1], c[2])));
                assertEquals(c[3], out.topic(), c[2]);
                assertEquals("A-1", out.key());
                JsonNode v = wireValue(out);
                assertEquals(c[2], v.get("type").asText());
                assertEquals(TENANT, v.get("tenantid").asText());
                assertEquals(c[2], wireHeader(out, "ce_type"));
            } finally {
                r.close();
            }
        }
    }

    @Test
    void typeOfAnotherContextIsNotRoutedIntoTheServicesTopics() {
        EventRouter<SourceRecord> r = typeRouter("sales");
        try {
            SourceRecord out = r.apply(outboxInsert("sales", "order", "dev.passage.fulfillment.packingslip.posted.v1"));
            assertEquals("dev.passage.fulfillment.packingslip.posted.v1", out.topic(),
                    "an unmatched type keeps its own name, which no debezium ACL allows");
        } finally {
            r.close();
        }
    }

    @Test
    @SuppressWarnings("rawtypes")
    void discoverableThroughServiceLoader() {
        boolean found = false;
        for (Transformation t : ServiceLoader.load(Transformation.class)) {
            found |= t instanceof CloudEventsEnricher;
        }
        assertTrue(found, "META-INF/services lists the SMT for plugin.discovery=service_load");
    }
}

package dev.passage.connect.cloudevents;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertThrows;

import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;

import org.apache.kafka.common.config.ConfigException;
import org.apache.kafka.connect.errors.DataException;
import org.apache.kafka.connect.header.ConnectHeaders;
import org.apache.kafka.connect.source.SourceRecord;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class CloudEventsEnricherTest {

    static final String TENANT = "0190a1b2-0000-7000-8000-000000000001";
    static final String LE = "0190a1b2-0000-7000-8000-0000000000aa";
    CloudEventsEnricher<SourceRecord> smt;

    @BeforeEach
    void setUp() {
        smt = new CloudEventsEnricher<>();
        smt.configure(Map.of("dataschema.template", "https://schemas.passage.dev/{type}.json"));
    }

    static Map<String, Object> event() {
        Map<String, Object> e = new LinkedHashMap<>();
        e.put("id", "01JABCDEF");
        e.put("source", "/services/batch");
        e.put("type", "dev.passage.manufacturing.machineevent.recorded.v1");
        e.put("subject", "machine/42");
        e.put("time", "2026-11-03T10:00:00Z");
        e.put("data", new LinkedHashMap<>(Map.of("legalEntityId", LE, "n", 1)));
        return e;
    }

    static SourceRecord record(Object value, String tenantHeader) {
        ConnectHeaders h = new ConnectHeaders();
        if (tenantHeader != null) {
            h.addString("tenantid", tenantHeader);
        }
        return new SourceRecord(null, null, "passage.platform.machineevent", null, null, "agg-1", null, value, null, h);
    }

    static String header(SourceRecord r, String name) {
        var h = r.headers().lastWithName(name);
        if (h == null) {
            return null;
        }
        Object v = h.value();
        return v instanceof byte[] b ? new String(b, StandardCharsets.UTF_8) : (String) v;
    }

    @SuppressWarnings("unchecked")
    static Map<String, Object> value(SourceRecord r) {
        return (Map<String, Object>) r.value();
    }

    @Test
    void completesEnvelopeAndCopiesHeaders() {
        SourceRecord out = smt.apply(record(event(), TENANT));
        Map<String, Object> v = value(out);
        assertEquals("1.0", v.get("specversion"));
        assertEquals("application/json", v.get("datacontenttype"));
        assertEquals(TENANT, v.get("tenantid"));
        assertEquals(LE, v.get("legalentityid"));
        assertEquals("https://schemas.passage.dev/dev.passage.manufacturing.machineevent.recorded.v1.json", v.get("dataschema"));
        assertEquals("dev.passage.manufacturing.machineevent.recorded.v1", header(out, "ce_type"));
        assertEquals("01JABCDEF", header(out, "ce_id"));
        assertEquals("/services/batch", header(out, "ce_source"));
        assertEquals("machine/42", header(out, "ce_subject"));
        assertEquals("2026-11-03T10:00:00Z", header(out, "ce_time"));
        assertEquals(TENANT, header(out, "ce_tenantid"));
        assertEquals(LE, header(out, "ce_legalentityid"));
        assertEquals("1.0", header(out, "ce_specversion"));
        assertEquals("application/cloudevents+json", header(out, "content-type"));
        assertEquals(TENANT, header(out, "tenantid"), "router header is kept");
        assertEquals("agg-1", out.key());
        assertEquals("passage.platform.machineevent", out.topic());
    }

    @Test
    void flattensHeadersJsonbWithoutOverwriting() {
        Map<String, Object> e = event();
        e.put("correlationid", "keep-me");
        e.put("headers", new LinkedHashMap<>(Map.of(
                "correlationId", "ignored", "causationid", "wf-123", "traceparent", "00-abc-def-01",
                "actor", "user:7", "type", "dev.passage.evil.x.y.v1", "bad_name", "x", "nested", Map.of("a", 1))));
        Map<String, Object> v = value(smt.apply(record(e, TENANT)));
        assertEquals("keep-me", v.get("correlationid"));
        assertEquals("wf-123", v.get("causationid"));
        assertEquals("00-abc-def-01", v.get("traceparent"));
        assertEquals("user:7", v.get("actor"));
        assertEquals("dev.passage.manufacturing.machineevent.recorded.v1", v.get("type"));
        assertFalse(v.containsKey("headers"));
        assertFalse(v.containsKey("bad_name"));
        assertFalse(v.containsKey("nested"));
    }

    @Test
    void keepsProducerAttributes() {
        Map<String, Object> e = event();
        e.put("tenantid", "from-value");
        e.put("legalentityid", "le-value");
        e.put("dataschema", "https://example.test/s");
        e.put("datacontenttype", "application/cloudevents+json");
        Map<String, Object> v = value(smt.apply(record(e, TENANT)));
        assertEquals("from-value", v.get("tenantid"));
        assertEquals("le-value", v.get("legalentityid"));
        assertEquals("https://example.test/s", v.get("dataschema"));
        assertEquals("application/cloudevents+json", v.get("datacontenttype"));
    }

    @Test
    void sourceDefaultAppliesWhenMissing() {
        Map<String, Object> e = event();
        e.remove("source");
        assertEquals("passage", header(smt.apply(record(e, TENANT)), "ce_source"));
    }

    @Test
    void snakeCaseLegalEntityIsRead() {
        Map<String, Object> e = event();
        e.put("data", new LinkedHashMap<>(Map.of("legal_entity_id", LE)));
        assertEquals(LE, value(smt.apply(record(e, TENANT))).get("legalentityid"));
    }

    @Test
    void noLegalEntityMeansNoAttribute() {
        Map<String, Object> e = event();
        e.put("data", new LinkedHashMap<>(Map.of("n", 1)));
        SourceRecord out = smt.apply(record(e, TENANT));
        assertFalse(value(out).containsKey("legalentityid"));
        assertNull(header(out, "ce_legalentityid"));
    }

    @Test
    void idempotentAndReplacesStaleCeHeaders() {
        SourceRecord in = record(event(), TENANT);
        in.headers().addString("ce_type", "stale");
        in.headers().addString("content-type", "text/plain");
        SourceRecord once = smt.apply(in);
        SourceRecord twice = smt.apply(once);
        assertEquals(value(once), value(twice));
        assertEquals(1, countHeaders(twice, "ce_type"));
        assertEquals(1, countHeaders(twice, "content-type"));
        assertEquals("application/cloudevents+json", header(twice, "content-type"));
        assertEquals("dev.passage.manufacturing.machineevent.recorded.v1", header(twice, "ce_type"));
    }

    static int countHeaders(SourceRecord r, String name) {
        int n = 0;
        for (var h : r.headers()) {
            if (h.key().equals(name)) {
                n++;
            }
        }
        return n;
    }

    @Test
    void doesNotMutateInputValue() {
        Map<String, Object> e = event();
        Map<String, Object> copy = new LinkedHashMap<>(e);
        smt.apply(record(e, TENANT));
        assertEquals(copy, e);
    }

    @Test
    void tombstoneAndNonMapPassThrough() {
        SourceRecord tomb = record(null, TENANT);
        assertSame(tomb, smt.apply(tomb));
        SourceRecord str = record("not a map", TENANT);
        assertSame(str, smt.apply(str));
    }

    @Test
    void rejectsNonConformingEvents() {
        for (String type : new String[] {null, "", "machine.recorded", "dev.passage.a.b.v1", "dev.passage.Mfg.x.y.v1", "dev.passage.a.b.c.v"}) {
            Map<String, Object> e = event();
            if (type == null) {
                e.remove("type");
            } else {
                e.put("type", type);
            }
            assertThrows(DataException.class, () -> smt.apply(record(e, TENANT)), "type " + type);
        }
        Map<String, Object> noId = event();
        noId.remove("id");
        assertThrows(DataException.class, () -> smt.apply(record(noId, TENANT)));
        Map<String, Object> noTime = event();
        noTime.remove("time");
        assertThrows(DataException.class, () -> smt.apply(record(noTime, TENANT)));
        Map<String, Object> badSpec = event();
        badSpec.put("specversion", "0.3");
        assertThrows(DataException.class, () -> smt.apply(record(badSpec, TENANT)));
    }

    @Test
    void rejectsMissingTenant() {
        assertThrows(DataException.class, () -> smt.apply(record(event(), null)));
    }

    @Test
    void tenantHeaderNameIsConfigurable() {
        CloudEventsEnricher<SourceRecord> other = new CloudEventsEnricher<>();
        other.configure(Map.of("tenant.header", "x-tenant"));
        SourceRecord in = new SourceRecord(null, null, "t", null, null, "k", null, event(), null,
                new ConnectHeaders().addString("x-tenant", TENANT));
        assertEquals(TENANT, value(other.apply(in)).get("tenantid"));
    }

    @Test
    void configDefaultsAndValidation() {
        CloudEventsEnricher<SourceRecord> d = new CloudEventsEnricher<>();
        d.configure(Map.of());
        Map<String, Object> v = value(d.apply(record(event(), TENANT)));
        assertFalse(v.containsKey("dataschema"));
        assertNotNull(d.config().configKeys().get("source.default"));
        assertEquals(java.util.Set.of("source.default", "dataschema.template", "headers.field", "tenant.header"), d.config().names());
        assertThrows(ConfigException.class, () -> new CloudEventsEnricher<SourceRecord>().configure(Map.of("source.default", 5)));
    }
}

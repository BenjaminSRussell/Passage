package main

import (
	"context"
	"strings"
	"testing"
)

func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestLoadConfig_Validation(t *testing.T) {
	base := map[string]string{"EDGE_SITE": "plant-1", "EDGE_ID": "edge-7"}
	tests := []struct {
		name     string
		override map[string]string
		wantErr  string
		hours    int
	}{
		{"defaults", nil, "", defaultBufferHrs},
		{"missing site", map[string]string{"EDGE_SITE": ""}, "EDGE_SITE is required", 0},
		{"missing edge id", map[string]string{"EDGE_ID": ""}, "EDGE_ID is required", 0},
		{"admin not loopback", map[string]string{"EDGE_ADMIN_ADDR": "0.0.0.0:8081"}, "loopback", 0},
		{"buffer hours too high", map[string]string{"IOT_BUFFER_HOURS": "169"}, "IOT_BUFFER_HOURS", 0},
		{"buffer hours not a number", map[string]string{"IOT_BUFFER_HOURS": "a day"}, "IOT_BUFFER_HOURS", 0},
		{"buffer hours set", map[string]string{"IOT_BUFFER_HOURS": "48"}, "", 48},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vals := map[string]string{}
			for k, v := range base {
				vals[k] = v
			}
			for k, v := range tc.override {
				vals[k] = v
			}
			cfg, err := loadConfig(env(vals))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.BufferHours != tc.hours {
					t.Fatalf("buffer hours %d, want %d", cfg.BufferHours, tc.hours)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfig_RuntimeSettings(t *testing.T) {
	node := "0195f0a0-0000-7000-8000-0000000000a1"
	base := map[string]string{"EDGE_SITE": "plant-1", "EDGE_ID": "edge-7"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cfg, err := loadConfig(env(with(map[string]string{"EDGE_SHOPFLOOR_URL": "https://shopfloor.plant/", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "IOT_BUFFER_CAPACITY": "5000", "EDGE_SIM_MACHINES": "4"})))
	if err != nil || cfg.ShopfloorURL != "https://shopfloor.plant" || cfg.BufferCapacity != 5000 || cfg.SimMachines != 4 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	if cfg, err := loadConfig(env(base)); err != nil || cfg.ShopfloorURL != "" || cfg.BufferCapacity != 200_000 {
		t.Fatalf("admin-only cfg = %+v, %v", cfg, err)
	}
	for name, extra := range map[string]map[string]string{
		"cleartext outside dev": {"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "http://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator"},
		"missing node id":       {"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_SOURCE": "simulator"},
		"no source adapter":     {"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "opcua"},
		"capacity too small":    {"IOT_BUFFER_CAPACITY": "10"},
		"opcua writer":          {"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "EDGE_WRITER": "opcua"},
		"cert dir with http":    {"PASSAGE_ENV": "dev", "EDGE_SHOPFLOOR_URL": "http://localhost", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "EDGE_CERT_DIR": "/certs"},
		"token without a dir":   {"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "EDGE_ENROLLMENT_TOKEN_FILE": "/t"},
	} {
		if _, err := loadConfig(env(with(extra))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if cfg, err := loadConfig(env(with(map[string]string{"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "EDGE_WRITER": "simulator", "EDGE_RECIPE_AUTO_COMPLETE": "true"}))); err != nil || cfg.Writer != "simulator" || !cfg.RecipeAutoComplete {
		t.Errorf("writer settings: %+v, %v", cfg, err)
	}
	if cfg, err := loadConfig(env(with(map[string]string{"EDGE_SHOPFLOOR_URL": "https://shopfloor", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator", "EDGE_CERT_DIR": "/certs", "EDGE_ENROLLMENT_TOKEN_FILE": "/t"}))); err != nil || cfg.CertDir != "/certs" || cfg.EnrollTokenFile != "/t" {
		t.Errorf("enrollment settings: %+v, %v", cfg, err)
	}
	if _, err := loadConfig(env(with(map[string]string{"PASSAGE_ENV": "dev", "EDGE_SHOPFLOOR_URL": "http://localhost:8080", "EDGE_NODE_ID": node, "EDGE_SOURCE": "simulator"}))); err != nil {
		t.Errorf("http in dev refused: %v", err)
	}
}

func TestEnrollIfNeeded_NeedsACertificateOrAToken(t *testing.T) {
	cfg := config{CertDir: t.TempDir(), ShopfloorURL: "https://shopfloor.invalid", NodeID: "n"}
	if _, err := enrollIfNeeded(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "EDGE_ENROLLMENT_TOKEN_FILE") {
		t.Fatalf("no certificate and no token: %v", err)
	}
	cfg.EnrollTokenFile = t.TempDir() + "/missing"
	if _, err := enrollIfNeeded(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "read EDGE_ENROLLMENT_TOKEN_FILE") {
		t.Fatalf("missing token file: %v", err)
	}
}

func TestLoadConfig_AdaptersBufferAndPLC(t *testing.T) {
	node := "0195f0a0-0000-7000-8000-0000000000a1"
	cfg := func(extra map[string]string) (config, error) {
		m := map[string]string{"EDGE_SITE": "plant-1", "EDGE_ID": "edge-7", "PASSAGE_ENV": "dev", "EDGE_SHOPFLOOR_URL": "http://localhost:8080", "EDGE_NODE_ID": node}
		for k, v := range extra {
			m[k] = v
		}
		return loadConfig(env(m))
	}
	c, err := cfg(map[string]string{"EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://plc:4840", "EDGE_OPCUA_INTERVAL_MS": "250", "EDGE_BUFFER_FILE": "/var/lib/iot-edge/buffer.log", "EDGE_WRITER": "opcua,mqtt", "EDGE_MQTT_BROKER": "broker:1883"})
	if err != nil || c.OPCUA.Endpoint != "opc.tcp://plc:4840" || c.OPCUA.Interval.Milliseconds() != 250 || c.BufferFile == "" || c.MQTTBroker != "broker:1883" {
		t.Fatalf("opcua source: %+v, %v", c, err)
	}
	if c, err := cfg(map[string]string{"EDGE_SOURCE": "mqtt", "EDGE_MQTT_BROKER": "broker:1883"}); err != nil || c.MQTTClientID != "passage-edge-7" {
		t.Fatalf("mqtt source: %+v, %v", c, err)
	}
	if c, err := cfg(map[string]string{"EDGE_SOURCE": "simulator", "EDGE_PLC_ADDR": "simulator", "EDGE_WAREHOUSE_URL": "http://warehouse:8080"}); err != nil || c.PLCStation != "PLC" {
		t.Fatalf("plc: %+v, %v", c, err)
	}
	for name, extra := range map[string]map[string]string{
		"opcua needs an endpoint":         {"EDGE_SOURCE": "opcua"},
		"opcua security needs a cert":     {"EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840", "EDGE_OPCUA_SECURITY_POLICY": "Basic256Sha256", "EDGE_OPCUA_SECURITY_MODE": "SignAndEncrypt"},
		"opcua interval out of range":     {"EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840", "EDGE_OPCUA_INTERVAL_MS": "5"},
		"mqtt needs a broker":             {"EDGE_SOURCE": "mqtt"},
		"opcua security None outside dev": {"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "https://s", "EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840"},
		"opcua writer None outside dev":   {"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "https://s", "EDGE_SOURCE": "simulator", "EDGE_WRITER": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840"},
		"opcua user over None in dev":     {"EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840", "EDGE_OPCUA_USER": "op"},
		"mqtt without tls outside dev":    {"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "https://s", "EDGE_SOURCE": "mqtt", "EDGE_MQTT_BROKER": "b:8883"},
		"unknown writer":                  {"EDGE_SOURCE": "simulator", "EDGE_WRITER": "modbus"},
		"plc station too long":            {"EDGE_SOURCE": "simulator", "EDGE_PLC_ADDR": "simulator", "EDGE_PLC_STATION": "TOOLONGNAME", "EDGE_WAREHOUSE_URL": "http://w"},
		"plc needs a warehouse":           {"EDGE_SOURCE": "simulator", "EDGE_PLC_ADDR": "plc:2000"},
		"plc address without a port":      {"EDGE_SOURCE": "simulator", "EDGE_PLC_ADDR": "plc", "EDGE_WAREHOUSE_URL": "http://w"},
		"cleartext warehouse outside dev": {"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "https://s", "EDGE_SOURCE": "simulator", "EDGE_PLC_ADDR": "plc:2000", "EDGE_WAREHOUSE_URL": "http://w"},
	} {
		if _, err := cfg(extra); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Outside dev the OPC-UA channel must be secured; a secured one is accepted.
	prod := map[string]string{"PASSAGE_ENV": "prod", "EDGE_SHOPFLOOR_URL": "https://s", "EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840"}
	if _, err := cfg(prod); err == nil || !strings.Contains(err.Error(), "EDGE_OPCUA_SECURITY_POLICY must not be None outside dev") {
		t.Fatalf("opcua None outside dev: %v", err)
	}
	prod["EDGE_OPCUA_SECURITY_POLICY"], prod["EDGE_OPCUA_SECURITY_MODE"] = "Basic256Sha256", "SignAndEncrypt"
	prod["EDGE_OPCUA_CERT_FILE"], prod["EDGE_OPCUA_KEY_FILE"], prod["EDGE_OPCUA_USER"] = "/c.pem", "/k.pem", "op"
	if c, err := cfg(prod); err != nil && strings.Contains(err.Error(), "EDGE_OPCUA") {
		t.Fatalf("a secured opcua channel outside dev: %v", err)
	} else if err == nil && c.OPCUA.Username != "op" {
		t.Fatalf("opcua user %q", c.OPCUA.Username)
	}
	if _, err := cfg(map[string]string{"EDGE_SOURCE": "opcua", "EDGE_OPCUA_ENDPOINT": "opc.tcp://p:4840", "EDGE_OPCUA_USER": "op"}); err == nil || !strings.Contains(err.Error(), "secured channel") {
		t.Fatalf("an opcua user name over None in dev: %v", err)
	}
}

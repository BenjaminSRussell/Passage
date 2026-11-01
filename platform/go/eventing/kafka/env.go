package kafka

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

func ConfigFromEnv(getenv func(string) string, defaultGroup, clientID string, topics []string) (Config, error) {
	brokers := strings.FieldsFunc(getenv("KAFKA_BROKERS"), func(r rune) bool { return r == ',' || r == ' ' })
	if len(brokers) == 0 {
		return Config{}, errors.New("KAFKA_BROKERS is required")
	}
	cfg := Config{
		Brokers: brokers, Group: defaultGroup, Topics: topics, ClientID: clientID,
		Username: getenv("KAFKA_USERNAME"), Password: getenv("KAFKA_PASSWORD"),
	}
	if group := getenv("KAFKA_GROUP"); group != "" {
		cfg.Group = group
	}
	if file := getenv("KAFKA_PASSWORD_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return Config{}, fmt.Errorf("read KAFKA_PASSWORD_FILE: %w", err)
		}
		cfg.Password = strings.TrimSpace(string(raw))
	}
	if cfg.Username != "" && cfg.Password == "" {
		return Config{}, errors.New("KAFKA_USERNAME needs KAFKA_PASSWORD or KAFKA_PASSWORD_FILE")
	}
	if ca := getenv("KAFKA_TLS_CA_FILE"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return Config{}, fmt.Errorf("read KAFKA_TLS_CA_FILE: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return Config{}, errors.New("KAFKA_TLS_CA_FILE holds no certificate")
		}
		cfg.TLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return cfg, nil
}

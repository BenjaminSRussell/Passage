package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/BenjaminSRussell/passage/platform/eventing"
)

const (
	HeaderDeadLetterReason = "x-dlq-reason"
	HeaderOriginTopic      = "x-origin-topic"
	HeaderOriginPartition  = "x-origin-partition"
	HeaderOriginOffset     = "x-origin-offset"
)

type Config struct {
	Brokers  []string
	Group    string
	Topics   []string
	ClientID string
	Username string
	Password string
	TLS      *tls.Config
}

type Client struct{ cl *kgo.Client }

func New(cfg Config) (*Client, error) {
	if len(cfg.Brokers) == 0 || cfg.Group == "" || len(cfg.Topics) == 0 {
		return nil, fmt.Errorf("brokers, group and topics are required: %w", eventing.ErrInvalidArgument)
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumerGroup(cfg.Group), kgo.ConsumeTopics(cfg.Topics...),
		kgo.DisableAutoCommit(), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.RequiredAcks(kgo.AllISRAcks()),
	}
	if cfg.ClientID != "" {
		opts = append(opts, kgo.ClientID(cfg.ClientID))
	}
	if cfg.TLS != nil {
		opts = append(opts, kgo.DialTLSConfig(cfg.TLS))
	}
	if cfg.Username != "" {
		opts = append(opts, kgo.SASL(scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha512Mechanism()))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}
	return &Client{cl: cl}, nil
}

func (c *Client) Close() { c.cl.Close() }

func (c *Client) Ping(ctx context.Context) error { return c.cl.Ping(ctx) }

func (c *Client) Poll(ctx context.Context) ([]eventing.Record, error) {
	fetches := c.cl.PollFetches(ctx)
	if fetches.IsClientClosed() {
		return nil, errors.New("kafka client closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errs := fetches.Errors(); len(errs) > 0 {
		return nil, fmt.Errorf("fetch %s/%d: %w", errs[0].Topic, errs[0].Partition, errs[0].Err)
	}
	records := make([]eventing.Record, 0, fetches.NumRecords())
	fetches.EachRecord(func(r *kgo.Record) { records = append(records, fromKgo(r)) })
	return records, nil
}

func (c *Client) Commit(ctx context.Context, r eventing.Record) error {
	raw, ok := r.Raw.(*kgo.Record)
	if !ok {
		return fmt.Errorf("record has no kafka handle: %w", eventing.ErrInvalidArgument)
	}
	return c.cl.CommitRecords(ctx, raw)
}

func (c *Client) Send(ctx context.Context, topic string, r eventing.Record, reason string) error {
	return c.cl.ProduceSync(ctx, deadLetterRecord(topic, r, reason)).FirstErr()
}

func fromKgo(r *kgo.Record) eventing.Record {
	headers := make(map[string]string, len(r.Headers))
	for _, h := range r.Headers {
		headers[strings.ToLower(h.Key)] = string(h.Value)
	}
	return eventing.Record{Topic: r.Topic, Partition: r.Partition, Offset: r.Offset, Key: r.Key, Value: r.Value, Headers: headers, Raw: r}
}

func deadLetterRecord(topic string, r eventing.Record, reason string) *kgo.Record {
	headers := make([]kgo.RecordHeader, 0, len(r.Headers)+4)
	for k, v := range r.Headers {
		headers = append(headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	headers = append(headers,
		kgo.RecordHeader{Key: HeaderDeadLetterReason, Value: []byte(reason)},
		kgo.RecordHeader{Key: HeaderOriginTopic, Value: []byte(r.Topic)},
		kgo.RecordHeader{Key: HeaderOriginPartition, Value: []byte(fmt.Sprint(r.Partition))},
		kgo.RecordHeader{Key: HeaderOriginOffset, Value: []byte(fmt.Sprint(r.Offset))},
	)
	return &kgo.Record{Topic: topic, Key: r.Key, Value: r.Value, Headers: headers}
}

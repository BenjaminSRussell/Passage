package failover

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// KubeServer addresses one region's Kubernetes API. The token is read from TokenFile on every call (a projected
// service-account token rotates); Token is for tests and short-lived use.
type KubeServer struct {
	Server    string `json:"server"`
	TokenFile string `json:"tokenFile,omitempty"`
	Token     string `json:"token,omitempty"`
	CAFile    string `json:"caFile,omitempty"`
}

// Kube is a minimal Kubernetes API client over net/http: merge patches, reads and deletes of custom resources. It exists so
// the executors need no client-go, and a fake API server (httptest) tests them.
type Kube struct {
	Servers map[string]KubeServer
	// HTTP overrides the client (tests). Default: a client with the region's CA.
	HTTP *http.Client
}

func (k *Kube) client(s KubeServer) (*http.Client, error) {
	if k.HTTP != nil {
		return k.HTTP, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificate in %s", s.CAFile)
		}
		cfg.RootCAs = pool
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}, nil
}

func (k *Kube) do(ctx context.Context, region, method, path, contentType string, body []byte) (int, []byte, error) {
	s, ok := k.Servers[region]
	if !ok {
		return 0, nil, fmt.Errorf("no Kubernetes API configured for region %q", region)
	}
	token := s.Token
	if s.TokenFile != "" {
		b, err := os.ReadFile(s.TokenFile)
		if err != nil {
			return 0, nil, err
		}
		token = strings.TrimSpace(string(b))
	}
	c, err := k.client(s)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(s.Server, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func statusErr(what string, code int, body []byte) error {
	return fmt.Errorf("%s: Kubernetes API answered %d: %s", what, code, strings.TrimSpace(string(bytes.TrimSpace(body[:min(len(body), 300)]))))
}

// MergePatch applies a JSON merge patch (RFC 7386); a null removes the field.
func (k *Kube) MergePatch(ctx context.Context, region, path string, patch any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	code, body, err := k.do(ctx, region, http.MethodPatch, path, "application/merge-patch+json", b)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return statusErr("patch "+path, code, body)
	}
	return nil
}

// Get reads path into out.
func (k *Kube) Get(ctx context.Context, region, path string, out any) error {
	code, body, err := k.do(ctx, region, http.MethodGet, path, "", nil)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return statusErr("get "+path, code, body)
	}
	return json.Unmarshal(body, out)
}

// Delete removes path; a missing object is success (idempotent).
func (k *Kube) Delete(ctx context.Context, region, path string) error {
	code, body, err := k.do(ctx, region, http.MethodDelete, path, "", nil)
	if err != nil {
		return err
	}
	if code/100 != 2 && code != http.StatusNotFound {
		return statusErr("delete "+path, code, body)
	}
	return nil
}

// CNPG fences and promotes the CloudNativePG cluster of a region through the Kubernetes API. The fence is the
// cnpg.io/fencedInstances annotation the chart renders for role FENCED; the promotion drops spec.replica, as the chart
// does for role PRIMARY, so a later Argo CD sync of the region files finds the cluster already in the target state.
type CNPG struct {
	Kube      *Kube
	Namespace string
	Cluster   string
	// PollEvery and Timeout bound the wait for the promoted cluster (defaults 5 s and 10 min).
	PollEvery time.Duration
	Timeout   time.Duration
}

func (c CNPG) path(extra string) string {
	return fmt.Sprintf("/apis/postgresql.cnpg.io/v1/namespaces/%s/clusters/%s%s", url.PathEscape(c.Namespace), url.PathEscape(c.Cluster), extra)
}

// FenceRegion implements Fencer.
func (c CNPG) FenceRegion(ctx context.Context, region string) error {
	return c.Kube.MergePatch(ctx, region, c.path(""), map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/fencedInstances": `["*"]`}},
	})
}

// Promote implements Promoter: unfence, drop spec.replica, then wait until the cluster reports a healthy primary.
func (c CNPG) Promote(ctx context.Context, region string) error {
	if err := c.Kube.MergePatch(ctx, region, c.path(""), map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/fencedInstances": nil}},
		"spec":     map[string]any{"replica": nil},
	}); err != nil {
		return err
	}
	every, timeout := c.PollEvery, c.Timeout
	if every <= 0 {
		every = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		var cl struct {
			Spec struct {
				Replica json.RawMessage `json:"replica"`
			} `json:"spec"`
			Status struct {
				Phase          string `json:"phase"`
				CurrentPrimary string `json:"currentPrimary"`
			} `json:"status"`
		}
		if err := c.Kube.Get(ctx, region, c.path(""), &cl); err != nil {
			return err
		}
		replica := strings.TrimSpace(string(cl.Spec.Replica))
		if (replica == "" || replica == "null") && cl.Status.CurrentPrimary != "" && strings.Contains(strings.ToLower(cl.Status.Phase), "healthy") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster %s in %s not a healthy primary after %s (phase %q)", c.Cluster, region, timeout, cl.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// MirrorMaker stops the KafkaMirrorMaker2 resource in the new primary (the chart renders it only next to its target).
type MirrorMaker struct {
	Kube      *Kube
	Namespace string
	Name      string
}

// StopInto implements Mirror.
func (m MirrorMaker) StopInto(ctx context.Context, newPrimary string) error {
	name := m.Name
	if name == "" {
		name = "passage-mm2"
	}
	return m.Kube.Delete(ctx, newPrimary, fmt.Sprintf("/apis/kafka.strimzi.io/v1beta2/namespaces/%s/kafkamirrormaker2s/%s", url.PathEscape(m.Namespace), url.PathEscape(name)))
}

// CommandRunner runs an external command and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the CommandRunner over os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// TemporalCLI updates the active cluster of each namespace with the temporal CLI
// (`temporal operator namespace update --active-cluster`), which is idempotent.
type TemporalCLI struct {
	Run        CommandRunner
	Address    string
	Namespaces []string
}

// ActivateIn implements Temporal. The Temporal cluster name equals the region name.
func (t TemporalCLI) ActivateIn(ctx context.Context, region string) error {
	if len(t.Namespaces) == 0 {
		return errors.New("no Temporal namespaces configured (OPS_TEMPORAL_NAMESPACES)")
	}
	for _, ns := range t.Namespaces {
		args := []string{"operator", "namespace", "update", "--namespace", ns, "--active-cluster", region, "--yes"}
		if t.Address != "" {
			args = append(args, "--address", t.Address)
		}
		if out, err := t.Run(ctx, "temporal", args...); err != nil {
			return fmt.Errorf("namespace %s: %w: %s", ns, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// DNSWebhook flips the traffic record through a provider-neutral webhook (the DNS provider is not fixed, ADR cloud
// agnostic): POST {record, target, ttlSeconds, weight} with a bearer token; any 2xx is success and the call is idempotent.
type DNSWebhook struct {
	URL    string
	Token  string
	Record string
	TTL    int
	HTTP   *http.Client
	// AllowInsecure permits a plain http URL (tests). The bearer token and the record must not cross an unencrypted link.
	AllowInsecure bool
}

// Flip implements DNS.
func (d DNSWebhook) Flip(ctx context.Context, region string) error {
	if d.URL == "" {
		return errors.New("no DNS webhook configured (OPS_DNS_WEBHOOK_URL)")
	}
	if u, perr := url.Parse(d.URL); perr != nil || (u.Scheme != "https" && !d.AllowInsecure) {
		return errors.New("the DNS webhook URL must be https (OPS_DNS_WEBHOOK_URL)")
	}
	body, err := json.Marshal(map[string]any{"record": d.Record, "target": region, "ttlSeconds": d.TTL, "weight": 100})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	c := d.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("DNS webhook answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// CommandVerifier runs a verification command; exit status 0 is a clean chain. The literal {region} in an argument is
// replaced by the region. It wraps deploy/multi-region/gameday/baseline.py verify, so the check that the game day uses is
// the one the failover runs.
type CommandVerifier struct {
	Run  CommandRunner
	Argv []string
}

// Verify implements ChainVerifier.
func (v CommandVerifier) Verify(ctx context.Context, region string) error {
	if len(v.Argv) == 0 {
		return errors.New("no hash-chain verification command configured (OPS_CHAIN_VERIFY_CMD)")
	}
	args := make([]string, len(v.Argv)-1)
	for i, a := range v.Argv[1:] {
		args[i] = strings.ReplaceAll(a, "{region}", region)
	}
	if out, err := v.Run(ctx, v.Argv[0], args...); err != nil {
		return fmt.Errorf("ledger hash chain verification failed in %s (OPS-1704, Sev-1: keep user access blocked): %w: %s", region, err, strings.TrimSpace(string(out)))
	}
	return nil
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/failover"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// deps are the seams tests replace: the HTTP client, the executors and the database for the lag reporter.
type deps struct {
	httpClient *http.Client
	executors  func(getenv func(string) string) (failover.Executors, error)
	lagSource  func(ctx context.Context, getenv func(string) string) (LagSource, error)
	sleep      func(ctx context.Context, d time.Duration) error
}

func defaultDeps() deps {
	return deps{executors: executorsFromEnv, lagSource: pgLagSource, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func newEventID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "failover-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

func runFailover(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "approve":
			return runApprove(ctx, args[1:], out, getenv, d)
		case "resume":
			return runResume(ctx, args[1:], out, getenv, d)
		}
		return fmt.Errorf("unknown failover command %q", args[0])
	}
	fs := flag.NewFlagSet("failover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var to, event string
	var acceptLoss, dry bool
	var unreachable string
	fs.StringVar(&to, "to", "", "the PASSIVE region to promote")
	fs.StringVar(&event, "event", "", "failover event id (Idempotency-Key); reuse it to resume. Default: generated")
	fs.BoolVar(&acceptLoss, "accept-data-loss", false, "accept the lag as data loss (needs a second operator's approval)")
	fs.StringVar(&unreachable, "fence-unreachable", "", "reason: if the old primary's region cannot be reached, record the fence step as done anyway (needs --accept-data-loss and a second operator's approval)")
	fs.BoolVar(&dry, "dry-run", false, "print the plan and what each step would do; change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if to == "" {
		return errors.New("--to <region> is required")
	}
	if unreachable != "" && !acceptLoss {
		return errors.New("--fence-unreachable needs --accept-data-loss: a lost region means the target may miss its last transactions, and a second operator must approve")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	if dry {
		return dryRun(ctx, c, to, acceptLoss, out, getenv)
	}
	if event == "" {
		event = newEventID()
	}
	fmt.Fprintf(out, "failover event %s (rerun with --event %s to resume)\n", event, event)
	var applied struct {
		store.Failover
		Request *store.Request `json:"request"`
	}
	status, err := c.do(ctx, http.MethodPost, "/v1/ops/failover", map[string]string{"Idempotency-Key": event}, map[string]any{"to": to, "acceptDataLoss": acceptLoss}, &applied)
	if err != nil {
		return err
	}
	if status == http.StatusAccepted {
		fmt.Fprintf(out, "target lag %d s is over the limit: a second operator must approve within %s:\n  passage-ops failover approve --request %s\n",
			applied.Request.LagSeconds, store.RequestTTL, applied.Request.ID)
		return nil
	}
	return execute(ctx, c, event, out, getenv, d, unreachable)
}

func runApprove(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	fs := flag.NewFlagSet("failover approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var id, unreachable string
	fs.StringVar(&id, "request", "", "the failover request id to approve")
	fs.StringVar(&unreachable, "fence-unreachable", "", "reason: record the fence step as done if the old primary's region cannot be reached")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		return errors.New("--request <id> is required")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	var rec store.Failover
	if _, err := c.do(ctx, http.MethodPost, "/v1/ops/failover-requests/"+esc(id)+":approve", nil, nil, &rec); err != nil {
		return err
	}
	fmt.Fprintf(out, "approved; failover event %s\n", rec.EventID)
	return execute(ctx, c, rec.EventID, out, getenv, d, unreachable)
}

func runResume(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	fs := flag.NewFlagSet("failover resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var event, unreachable string
	fs.StringVar(&event, "event", "", "the failover event id")
	fs.StringVar(&unreachable, "fence-unreachable", "", "reason: record the fence step as done if the old primary's region cannot be reached (the failover must have been approved by a second operator)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if event == "" {
		return errors.New("--event <id> is required")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	return execute(ctx, c, event, out, getenv, d, unreachable)
}

// dryRun plans against the live region table and prints each step; it records nothing and calls no executor.
func dryRun(ctx context.Context, c *client, to string, acceptLoss bool, out io.Writer, getenv func(string) string) error {
	var list struct {
		Items         []domain.Region `json:"items"`
		MaxLagSeconds int             `json:"maxLagSeconds"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/v1/ops/regions", nil, nil, &list); err != nil {
		return err
	}
	plan, err := domain.PlanFailover(list.Items, domain.FailoverRequest{To: to, AcceptDataLoss: acceptLoss, Caller: "dry-run", Approver: "dry-run-approver", MaxLagSeconds: list.MaxLagSeconds})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "dry run: fail over %s -> %s (lag %d s, data loss accepted: %v)\n", plan.From, plan.To, plan.LagSeconds, plan.AcceptDataLoss)
	res, err := failover.Runner{DryRun: true}.Run(ctx, plan, nil)
	if err != nil {
		return err
	}
	printSteps(out, res)
	return nil
}

func printSteps(out io.Writer, res []failover.StepResult) {
	for _, r := range res {
		fmt.Fprintf(out, "  %d. %-26s %-8s %s\n", r.Order, r.Action, r.Status, r.Detail)
	}
}

// execute reads the recorded failover, runs the steps not yet DONE and reports each one.
func execute(ctx context.Context, c *client, event string, out io.Writer, getenv func(string) string, d deps, fenceUnreachable string) error {
	var rec store.FailoverDetail
	if _, err := c.do(ctx, http.MethodGet, "/v1/ops/failovers/"+esc(event), nil, nil, &rec); err != nil {
		return err
	}
	done := map[string]bool{}
	for _, s := range rec.Steps {
		if s.Status == failover.StatusDone {
			done[s.Action] = true
		}
	}
	if d.executors == nil {
		return errors.New("no executors configured")
	}
	exec, err := d.executors(getenv)
	if err != nil {
		return err
	}
	r := failover.Runner{Exec: exec, FenceUnreachable: fenceUnreachable, Report: func(ctx context.Context, res failover.StepResult) error {
		if res.Status == failover.StatusSkipped {
			return nil
		}
		body := map[string]any{"order": res.Order, "region": res.Region, "status": res.Status, "detail": res.Detail, "forced": res.Forced, "startedAt": res.StartedAt, "finishedAt": res.FinishedAt}
		_, err := c.do(ctx, http.MethodPut, "/v1/ops/failovers/"+esc(event)+"/steps/"+esc(res.Action), nil, body, nil)
		return err
	}}
	fmt.Fprintf(out, "executing %s -> %s\n", rec.Plan.From, rec.Plan.To)
	res, err := r.Run(ctx, rec.Plan, done)
	printSteps(out, res)
	if err != nil {
		fmt.Fprintf(out, "stopped; fix the cause and run: passage-ops failover resume --event %s\n", event)
		return err
	}
	fmt.Fprintln(out, "failover complete")
	return nil
}

// executorsFromEnv builds the real adapters from the environment.
//
//	OPS_KUBE_REGIONS      JSON object {"<region>": {"server", "tokenFile", "caFile"}} or @file
//	OPS_CNPG_NAMESPACE    default passage-data; OPS_CNPG_CLUSTER default passage-pg
//	OPS_KAFKA_NAMESPACE   default kafka
//	OPS_TEMPORAL_ADDRESS, OPS_TEMPORAL_NAMESPACES (comma separated)
//	OPS_DNS_WEBHOOK_URL, OPS_DNS_WEBHOOK_TOKEN, OPS_DNS_RECORD; DNS_TTL_S (default 60)
//	OPS_CHAIN_VERIFY_CMD  the verification command, a JSON array or a space-separated line; {region} is replaced
func executorsFromEnv(getenv func(string) string) (failover.Executors, error) {
	kube := &failover.Kube{Servers: map[string]failover.KubeServer{}}
	raw := getenv("OPS_KUBE_REGIONS")
	if strings.HasPrefix(raw, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
		if err != nil {
			return failover.Executors{}, fmt.Errorf("OPS_KUBE_REGIONS: %w", err)
		}
		raw = string(b)
	}
	if raw == "" {
		return failover.Executors{}, errors.New("OPS_KUBE_REGIONS is required: the Kubernetes API of each region")
	}
	if err := json.Unmarshal([]byte(raw), &kube.Servers); err != nil {
		return failover.Executors{}, fmt.Errorf("OPS_KUBE_REGIONS: %w", err)
	}
	def := func(k, v string) string {
		if s := getenv(k); s != "" {
			return s
		}
		return v
	}
	ttl := 60
	if v := getenv("DNS_TTL_S"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &ttl); err != nil {
			return failover.Executors{}, fmt.Errorf("DNS_TTL_S: %w", err)
		}
	}
	var ns []string
	for _, n := range strings.Split(getenv("OPS_TEMPORAL_NAMESPACES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			ns = append(ns, n)
		}
	}
	var argv []string
	if c := strings.TrimSpace(getenv("OPS_CHAIN_VERIFY_CMD")); strings.HasPrefix(c, "[") {
		if err := json.Unmarshal([]byte(c), &argv); err != nil {
			return failover.Executors{}, fmt.Errorf("OPS_CHAIN_VERIFY_CMD: %w", err)
		}
	} else {
		argv = strings.Fields(c)
	}
	cnpg := failover.CNPG{Kube: kube, Namespace: def("OPS_CNPG_NAMESPACE", "passage-data"), Cluster: def("OPS_CNPG_CLUSTER", "passage-pg")}
	return failover.Executors{
		Fence:    cnpg,
		Promote:  cnpg,
		Mirror:   failover.MirrorMaker{Kube: kube, Namespace: def("OPS_KAFKA_NAMESPACE", "kafka")},
		Temporal: failover.TemporalCLI{Run: failover.ExecRunner, Address: getenv("OPS_TEMPORAL_ADDRESS"), Namespaces: ns},
		DNS:      failover.DNSWebhook{URL: getenv("OPS_DNS_WEBHOOK_URL"), Token: getenv("OPS_DNS_WEBHOOK_TOKEN"), Record: getenv("OPS_DNS_RECORD"), TTL: ttl},
		Chain:    failover.CommandVerifier{Run: failover.ExecRunner, Argv: argv},
	}, nil
}

package domain

import (
	"fmt"
	"math"
	"math/big"
)

// The capacity model of docs/ops/capacity-model.md (FR-50.9), served by GET /v1/ops/capacity/estimate. It mirrors
// docs/ops/tools/capacity_estimate.py; the tests pin the same reference figures. Constants marked ASSUMED there stay
// assumed here until a load campaign measures them.
const (
	ModelVersion        = "v1"
	headroom            = 1.5
	passiveWarmFraction = 0.5
	apiPodReqPerS       = 80.0  // ASSUMED
	ledgerPodLinesPerS  = 600.0 // ASSUMED
	orderPodLinesPerS   = 30.0
	minOrderPods        = 3
	podVCPU             = 1.0  // ASSUMED
	podMemGiB           = 2.0  // ASSUMED
	nodeVCPU            = 6.0  // ASSUMED
	nodeMemGiB          = 24.0 // ASSUMED
	pgInitialGiB        = 500
	pgInstances         = 3
	regionCount         = 2
	kafkaMinBrokers     = 6
	kafkaMsgsPerBrokerS = 10_000.0 // ASSUMED
	overheadPercent     = 12
)

// CapacityInputs are the workload inputs; every field must be positive. DefaultCapacityInputs is the 10k-user reference tenant.
type CapacityInputs struct {
	Users                  int `json:"users"`
	ThinkTimeS             int `json:"thinkTimeS"`
	JournalLinesPerHour    int `json:"journalLinesPerHour"`
	JournalTargetLinesPerS int `json:"journalTargetLinesPerS"`
	OrderLinesPerHour      int `json:"orderLinesPerHour"`
	Scanners               int `json:"scanners"`
	ScanIntervalS          int `json:"scanIntervalS"`
	LedgerGBPerYear        int `json:"ledgerGbPerYear"`
	OrdersGBPerYear        int `json:"ordersGbPerYear"`
	ScansGBPerYear         int `json:"scansGbPerYear"`
	Years                  int `json:"years"`
	PeakKafkaMsgsPerS      int `json:"peakKafkaMsgsPerS"`
	ConsumerParallelism    int `json:"consumerParallelism"`
}

// DefaultCapacityInputs is the reference tenant of the capacity model.
func DefaultCapacityInputs() CapacityInputs {
	return CapacityInputs{Users: 10_000, ThinkTimeS: 10, JournalLinesPerHour: 1_000_000, JournalTargetLinesPerS: 5_000,
		OrderLinesPerHour: 100_000, Scanners: 2_000, ScanIntervalS: 5, LedgerGBPerYear: 1_200, OrdersGBPerYear: 200,
		ScansGBPerYear: 100, Years: 3, PeakKafkaMsgsPerS: 60_000, ConsumerParallelism: 8}
}

// Rates are optional provider prices in USD (decimal strings); only the parts with a rate are priced.
type Rates struct {
	NodeUSDMonth   string
	PgGiBUSDMonth  string
	BrokerUSDMonth string
}

// CapacityEstimate is the model output.
type CapacityEstimate struct {
	ModelVersion string `json:"modelVersion"`
	Derived      struct {
		InteractiveRequestsPerS float64 `json:"interactiveRequestsPerS"`
		JournalLinesPerSAverage float64 `json:"journalLinesPerSAverage"`
		JournalLinesPerSSizing  int     `json:"journalLinesPerSSizing"`
		OrderLinesPerS          float64 `json:"orderLinesPerS"`
		ScansPerS               float64 `json:"scansPerS"`
	} `json:"derived"`
	Pods struct {
		API     int `json:"api"`
		Ledger  int `json:"ledger"`
		Order   int `json:"order"`
		Primary int `json:"primary"`
		Passive int `json:"passive"`
	} `json:"pods"`
	Nodes struct {
		Primary int `json:"primary"`
		Passive int `json:"passive"`
	} `json:"nodes"`
	Postgres struct {
		InstancesPerCluster int `json:"instancesPerCluster"`
		StorageGiB          int `json:"storageGiB"`
	} `json:"postgres"`
	Kafka struct {
		Brokers               int `json:"brokers"`
		ReplicationFactor     int `json:"replicationFactor"`
		MinInsyncReplicas     int `json:"minInsyncReplicas"`
		PartitionsMinPerTopic int `json:"partitionsMinPerTopic"`
	} `json:"kafka"`
	CostUSDMonth string   `json:"costUsdMonth,omitempty"`
	CostPriced   []string `json:"costPriced,omitempty"`
}

func ceil(x float64) int { return int(math.Ceil(x - 1e-9)) }

func pods(rate, perPod float64) int { return ceil(float64(ceil(rate/perPod)) * headroom) }

func nodes(p int) int {
	return max(ceil(float64(p)*podVCPU/nodeVCPU), ceil(float64(p)*podMemGiB/nodeMemGiB))
}

func (in CapacityInputs) validate() error {
	fields := []struct {
		name string
		v    int
	}{
		{"users", in.Users}, {"thinkTimeS", in.ThinkTimeS}, {"journalLinesPerHour", in.JournalLinesPerHour},
		{"journalTargetLinesPerS", in.JournalTargetLinesPerS}, {"orderLinesPerHour", in.OrderLinesPerHour},
		{"scanners", in.Scanners}, {"scanIntervalS", in.ScanIntervalS}, {"ledgerGbPerYear", in.LedgerGBPerYear},
		{"ordersGbPerYear", in.OrdersGBPerYear}, {"scansGbPerYear", in.ScansGBPerYear}, {"years", in.Years},
		{"peakKafkaMsgsPerS", in.PeakKafkaMsgsPerS}, {"consumerParallelism", in.ConsumerParallelism},
	}
	for _, f := range fields {
		if f.v <= 0 {
			return fmt.Errorf("%s must be a positive integer", f.name)
		}
	}
	return nil
}

func parseRate(name, s string) (*big.Rat, error) {
	if s == "" {
		return nil, nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("%s must be a non-negative decimal", name)
	}
	return r, nil
}

// EstimateCapacity maps the inputs to pods, nodes, Postgres storage, Kafka brokers and, for the rates given, a monthly
// cost in both regions plus 12% overhead, rounded once to the cent half up (BR-50.10).
func EstimateCapacity(in CapacityInputs, rates Rates) (CapacityEstimate, error) {
	var out CapacityEstimate
	if err := in.validate(); err != nil {
		return out, err
	}
	nodeRate, err := parseRate("nodeUsdMonth", rates.NodeUSDMonth)
	if err != nil {
		return out, err
	}
	pgRate, err := parseRate("pgGibUsdMonth", rates.PgGiBUSDMonth)
	if err != nil {
		return out, err
	}
	brokerRate, err := parseRate("brokerUsdMonth", rates.BrokerUSDMonth)
	if err != nil {
		return out, err
	}
	reqS := float64(in.Users) / float64(in.ThinkTimeS)
	orderS := float64(in.OrderLinesPerHour) / 3600
	out.ModelVersion = ModelVersion
	out.Derived.InteractiveRequestsPerS = round2(reqS)
	out.Derived.JournalLinesPerSAverage = round2(float64(in.JournalLinesPerHour) / 3600)
	out.Derived.JournalLinesPerSSizing = in.JournalTargetLinesPerS
	out.Derived.OrderLinesPerS = round2(orderS)
	out.Derived.ScansPerS = round2(float64(in.Scanners) / float64(in.ScanIntervalS))

	out.Pods.API = pods(reqS, apiPodReqPerS)
	out.Pods.Ledger = pods(float64(in.JournalTargetLinesPerS), ledgerPodLinesPerS)
	out.Pods.Order = max(minOrderPods, pods(orderS, orderPodLinesPerS))
	out.Pods.Primary = out.Pods.API + out.Pods.Ledger + out.Pods.Order
	out.Pods.Passive = ceil(float64(out.Pods.Primary) * passiveWarmFraction)
	out.Nodes.Primary = nodes(out.Pods.Primary)
	out.Nodes.Passive = nodes(out.Pods.Passive)

	storageGB := (in.LedgerGBPerYear + in.OrdersGBPerYear + in.ScansGBPerYear) * in.Years
	out.Postgres.InstancesPerCluster = pgInstances
	out.Postgres.StorageGiB = max(pgInitialGiB, storageGB)
	out.Kafka.Brokers = max(kafkaMinBrokers, ceil(float64(in.PeakKafkaMsgsPerS)/kafkaMsgsPerBrokerS))
	out.Kafka.ReplicationFactor, out.Kafka.MinInsyncReplicas = 3, 2
	out.Kafka.PartitionsMinPerTopic = 2 * in.ConsumerParallelism

	total := new(big.Rat)
	priced := func(part string, rate *big.Rat, units int64) {
		if rate == nil {
			return
		}
		total.Add(total, new(big.Rat).Mul(rate, big.NewRat(units, 1)))
		out.CostPriced = append(out.CostPriced, part)
	}
	// CostPriced is alphabetical, as the Python model prints it.
	priced("kafka", brokerRate, int64(out.Kafka.Brokers*regionCount))
	priced("nodes", nodeRate, int64(out.Nodes.Primary+out.Nodes.Passive))
	priced("postgres", pgRate, int64(out.Postgres.StorageGiB*pgInstances*regionCount))
	if len(out.CostPriced) > 0 {
		total.Mul(total, big.NewRat(100+overheadPercent, 100))
		out.CostUSDMonth = roundCents(total)
	}
	return out, nil
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// roundCents renders r as dollars with two decimals, rounding half up.
func roundCents(r *big.Rat) string {
	cents := new(big.Rat).Mul(r, big.NewRat(100, 1))
	n := new(big.Int).Quo(cents.Num(), cents.Denom())
	rem := new(big.Rat).Sub(cents, new(big.Rat).SetInt(n))
	if rem.Cmp(big.NewRat(1, 2)) >= 0 {
		n.Add(n, big.NewInt(1))
	}
	s := n.String()
	for len(s) < 3 {
		s = "0" + s
	}
	return s[:len(s)-2] + "." + s[len(s)-2:]
}

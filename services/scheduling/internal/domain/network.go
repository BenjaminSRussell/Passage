package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// CodeNetworkInfeasible is SCHED-0009: an operation chain has no allowed
// successor within the network delay limit (BR-23.11). The run flags the
// order with FlagNetworkInfeasible.
const (
	CodeNetworkInfeasible = "SCHED-0009"
	FlagNetworkInfeasible = "NETWORK_INFEASIBLE"
)

// CodeNetworkWait is SCHED-0010: the wait exceeds the network maximum.
const CodeNetworkWait = "SCHED-0010"

// NetworkEdge is an allowed transition from resource From to resource To
// (FR-23.13). MaxWaitMinutes nil means no maximum wait. SecondaryResourceID
// names a resource the transfer also needs (a transfer line), empty if none.
type NetworkEdge struct {
	From                string `json:"fromResourceId"`
	To                  string `json:"toResourceId"`
	TransferMinutes     int    `json:"transferMinutes"`
	MaxWaitMinutes      *int   `json:"maxWaitMinutes"`
	SecondaryResourceID string `json:"secondaryResourceId,omitempty"`
}

// ResourceNetwork is a directed graph of resources. When operation n runs on
// resource r, operation n+1 may only run on a resource r' with an edge r -> r'.
type ResourceNetwork struct {
	Code  string
	Edges []NetworkEdge
	index map[[2]string]NetworkEdge
}

// NewResourceNetwork validates the edges and indexes them by resource pair.
func NewResourceNetwork(code string, edges []NetworkEdge) (ResourceNetwork, error) {
	if code == "" {
		return ResourceNetwork{}, errors.New("network needs a code")
	}
	n := ResourceNetwork{Code: code, Edges: edges, index: make(map[[2]string]NetworkEdge, len(edges))}
	for _, e := range edges {
		switch {
		case e.From == "" || e.To == "":
			return ResourceNetwork{}, fmt.Errorf("network %s: edge needs both resources", code)
		case e.From == e.To:
			return ResourceNetwork{}, fmt.Errorf("network %s: resource %s cannot feed itself", code, e.From)
		case e.TransferMinutes < 0:
			return ResourceNetwork{}, fmt.Errorf("network %s: transfer minutes must not be negative", code)
		case e.MaxWaitMinutes != nil && *e.MaxWaitMinutes < 0:
			return ResourceNetwork{}, fmt.Errorf("network %s: max wait must not be negative", code)
		case e.MaxWaitMinutes != nil && *e.MaxWaitMinutes < e.TransferMinutes:
			// The wait runs from the end of operation n and includes the
			// transfer, so a shorter maximum can never be met (BR-23.11).
			return ResourceNetwork{}, fmt.Errorf("network %s: edge %s->%s max wait must be at least its transfer minutes", code, e.From, e.To)
		case e.SecondaryResourceID == e.From || e.SecondaryResourceID == e.To:
			return ResourceNetwork{}, fmt.Errorf("network %s: edge %s->%s cannot use one of its own resources as the transfer resource", code, e.From, e.To)
		}
		k := [2]string{e.From, e.To}
		if _, dup := n.index[k]; dup {
			return ResourceNetwork{}, fmt.Errorf("network %s: edge %s->%s is listed twice", code, e.From, e.To)
		}
		n.index[k] = e
	}
	return n, nil
}

// Edge returns the transition from one resource to another.
func (n ResourceNetwork) Edge(from, to string) (NetworkEdge, bool) {
	e, ok := n.index[[2]string{from, to}]
	return e, ok
}

// Successors returns the candidates reachable from `from` over an edge, in the
// order BR-23.11 tries them: transfer minutes, then resource id.
func (n ResourceNetwork) Successors(from string, candidates []string) []NetworkEdge {
	var out []NetworkEdge
	for _, c := range candidates {
		if e, ok := n.Edge(from, c); ok {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TransferMinutes != out[j].TransferMinutes {
			return out[i].TransferMinutes < out[j].TransferMinutes
		}
		return out[i].To < out[j].To
	})
	return out
}

// ChainAllowed reports whether every consecutive pair of resources in path has
// an edge, which is the network invariant for a scheduled operation chain.
func (n ResourceNetwork) ChainAllowed(path []string) bool {
	for i := 1; i < len(path); i++ {
		if _, ok := n.Edge(path[i-1], path[i]); !ok {
			return false
		}
	}
	return true
}

// WaitOK checks the gap between the end of operation n and the start of
// operation n+1 against the edge: the gap must cover the transfer time and must
// not exceed the maximum wait (BR-23.11: start_{n+1} - end_n <= w).
func (e NetworkEdge) WaitOK(prevEnd, nextStart time.Time) bool {
	gap := nextStart.Sub(prevEnd)
	if gap < time.Duration(e.TransferMinutes)*time.Minute {
		return false
	}
	return e.MaxWaitMinutes == nil || gap <= time.Duration(*e.MaxWaitMinutes)*time.Minute
}

package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
)

const (
	CheckRowCount      = "row_count"
	CheckHashTotal     = "hash_total"
	CheckSampleCompare = "sample_compare"
)

type Check struct {
	Name   string
	OK     bool
	Detail string
}

type ReconcileReport struct {
	Source int
	Target int
	Checks []Check
}

func (r ReconcileReport) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return len(r.Checks) > 0
}

func Reconcile(source, target []Record, sampleSize int, seed int64) ReconcileReport {
	rep := ReconcileReport{Source: len(source), Target: len(target)}
	rep.Checks = append(rep.Checks, Check{CheckRowCount, len(source) == len(target), fmt.Sprintf("source %d target %d", len(source), len(target))})
	srcTotal, tgtTotal := hashTotal(source), hashTotal(target)
	rep.Checks = append(rep.Checks, Check{CheckHashTotal, srcTotal == tgtTotal, "order-independent SHA-256 over key and content hash"})
	mismatched := sampleMismatches(source, target, sampleSize, seed)
	rep.Checks = append(rep.Checks, Check{CheckSampleCompare, len(mismatched) == 0, fmt.Sprintf("%d sampled keys differ", len(mismatched))})
	return rep
}

func hashTotal(records []Record) string {
	hashes := make([]string, len(records))
	for i, r := range records {
		hashes[i] = r.Key + "|" + ContentHash(r.Fields)
	}
	sort.Strings(hashes)
	h := sha256.New()
	for _, x := range hashes {
		h.Write([]byte(x))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sampleMismatches(source, target []Record, sampleSize int, seed int64) []string {
	byKey := make(map[string]string, len(target))
	for _, r := range target {
		byKey[r.Key] = ContentHash(r.Fields)
	}
	sorted := append([]Record(nil), source...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	picks := rand.New(rand.NewSource(seed)).Perm(len(sorted))
	if sampleSize < len(picks) {
		picks = picks[:sampleSize]
	}
	var bad []string
	for _, i := range picks {
		if got, ok := byKey[sorted[i].Key]; !ok || got != ContentHash(sorted[i].Fields) {
			bad = append(bad, sorted[i].Key)
		}
	}
	return bad
}

#!/usr/bin/env python3
"""Capacity estimate (PSG-50, FR-50.9, capacity model in docs/ops/capacity-model.md).

Maps workload inputs (concurrent users, journal lines/h, order lines/h, scanners, storage growth) to pod counts,
nodes, Postgres storage, Kafka brokers and, when provider rates are given, a monthly cost. The formulas and constants
follow docs/ops/capacity-model.md. Constants marked ASSUMED there are reproduced here with the same marking and
must be replaced by measured values from the load campaign (load/) before a model version is published.

    python3 docs/ops/tools/capacity_estimate.py --users 10000 --journal-lines-per-hour 1000000 \\
        --order-lines-per-hour 100000 --scanners 2000 [--node-usd-month 610.00] \\
        [--pg-gib-usd-month 0.115] [--broker-usd-month 420.00]

Cost covers only the parts whose rate is given, in both regions, plus the 12% overhead. The passive region keeps a
Postgres replica cluster with the same instance count (chart values) and its own Kafka cluster, the MirrorMaker 2
target, assumed to be the same size [ASSUMED]. The output names the priced parts in `costPriced`, so an estimate
without a Postgres or Kafka rate is not mistaken for the whole bill.

The served endpoint `GET /api/v1/ops/capacity/estimate` belongs to the ops service and is not built here.

Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal

MODEL_VERSION = "v1"
HEADROOM = 1.5  # capacity-model.md: pods x 1.5 headroom
PASSIVE_WARM_FRACTION = 0.5  # passive region runs warm at 50%
API_POD_REQ_PER_S = 80  # [ASSUMED 80 req/s] per pod
LEDGER_POD_LINES_PER_S = 600  # [ASSUMED 600 lines/s] per pod
ORDER_POD_LINES_PER_S = 30  # per-pod budget from FR-50.1 / PRD 9.50.19
MIN_ORDER_PODS = 3  # one carries the load, three are deployed for availability
POD_VCPU = 1  # [ASSUMED] request per pod
POD_MEM_GIB = 2  # [ASSUMED] request per pod
NODE_VCPU = 6  # [ASSUMED 6 vCPU usable per node]
NODE_MEM_GIB = 24  # [ASSUMED 24 GiB usable per node]
PG_INITIAL_GIB = 500  # postgres.storageSize initial volume per region
PG_INSTANCES = 3  # 1 primary + 2 standbys (FR-50.2); the passive replica cluster has the same count (chart values)
REGIONS = 2  # active/passive pair
KAFKA_MIN_BROKERS = 6
KAFKA_MSGS_PER_BROKER_S = 10_000  # [ASSUMED] sustained produce per broker
OVERHEAD = Decimal("0.12")  # shared control-plane cost, cost-model.md
CENT = Decimal("0.01")


class CapacityError(ValueError):
    pass


@dataclass(frozen=True)
class Inputs:
    users: int = 10_000
    think_time_s: int = 10
    journal_lines_per_hour: int = 1_000_000
    journal_target_lines_per_s: int = 5_000  # sustained target used for sizing (18x the average)
    order_lines_per_hour: int = 100_000
    scanners: int = 2_000
    scan_interval_s: int = 5
    ledger_gb_per_year: int = 1_200
    orders_gb_per_year: int = 200
    scans_gb_per_year: int = 100
    years: int = 3
    peak_kafka_msgs_per_s: int = 60_000
    consumer_parallelism: int = 8  # [ASSUMED] for the partition floor (>= 2x consumers)

    def validate(self) -> None:
        for name, value in self.__dict__.items():
            if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
                raise CapacityError(f"{name} must be a positive integer")


def _ceil(x: float) -> int:
    return int(math.ceil(x - 1e-9))


def _pods(rate: float, per_pod: float) -> int:
    """pods = ceil(ceil(rate / per_pod) * 1.5), the rule in capacity-model.md."""
    return _ceil(_ceil(rate / per_pod) * HEADROOM)


def _nodes(pods: int) -> int:
    return max(_ceil(pods * POD_VCPU / NODE_VCPU), _ceil(pods * POD_MEM_GIB / NODE_MEM_GIB))


def _rate(value: Decimal | None, name: str) -> Decimal | None:
    if value is None:
        return None
    if not isinstance(value, Decimal) or not value.is_finite() or value < 0:
        raise CapacityError(f"{name} must be a finite non-negative decimal")
    return value


def estimate(
    inp: Inputs,
    node_usd_month: Decimal | None = None,
    pg_gib_usd_month: Decimal | None = None,
    broker_usd_month: Decimal | None = None,
) -> dict[str, object]:
    inp.validate()
    node_rate = _rate(node_usd_month, "node_usd_month")
    pg_rate = _rate(pg_gib_usd_month, "pg_gib_usd_month")
    broker_rate = _rate(broker_usd_month, "broker_usd_month")
    req_s = inp.users / inp.think_time_s
    journal_avg_s = inp.journal_lines_per_hour / 3600
    order_s = inp.order_lines_per_hour / 3600
    scans_s = inp.scanners / inp.scan_interval_s

    api_pods = _pods(req_s, API_POD_REQ_PER_S)
    ledger_pods = _pods(inp.journal_target_lines_per_s, LEDGER_POD_LINES_PER_S)
    order_pods = max(MIN_ORDER_PODS, _pods(order_s, ORDER_POD_LINES_PER_S))
    primary_pods = api_pods + ledger_pods + order_pods
    passive_pods = _ceil(primary_pods * PASSIVE_WARM_FRACTION)
    primary_nodes = _nodes(primary_pods)
    passive_nodes = _nodes(passive_pods)

    storage_gb = (inp.ledger_gb_per_year + inp.orders_gb_per_year + inp.scans_gb_per_year) * inp.years
    pg_gib = max(PG_INITIAL_GIB, _ceil(storage_gb))  # GB treated as GiB, conservative for sizing

    brokers = max(KAFKA_MIN_BROKERS, _ceil(inp.peak_kafka_msgs_per_s / KAFKA_MSGS_PER_BROKER_S))
    partitions_min = 2 * inp.consumer_parallelism

    out: dict[str, object] = {
        "modelVersion": MODEL_VERSION,
        "derived": {
            "interactiveRequestsPerS": round(req_s, 2),
            "journalLinesPerSAverage": round(journal_avg_s, 2),
            "journalLinesPerSSizing": inp.journal_target_lines_per_s,
            "orderLinesPerS": round(order_s, 2),
            "scansPerS": round(scans_s, 2),
        },
        "pods": {"api": api_pods, "ledger": ledger_pods, "order": order_pods, "primary": primary_pods,
                 "passive": passive_pods},
        "nodes": {"primary": primary_nodes, "passive": passive_nodes},
        "postgres": {"instancesPerCluster": PG_INSTANCES, "storageGiB": pg_gib},
        "kafka": {"brokers": brokers, "replicationFactor": 3, "minInsyncReplicas": 2,
                  "partitionsMinPerTopic": partitions_min},
    }
    parts: dict[str, Decimal] = {}
    if node_rate is not None:
        parts["nodes"] = Decimal(primary_nodes + passive_nodes) * node_rate
    if pg_rate is not None:
        parts["postgres"] = Decimal(pg_gib * PG_INSTANCES * REGIONS) * pg_rate
    if broker_rate is not None:
        parts["kafka"] = Decimal(brokers * REGIONS) * broker_rate
    if parts:
        # Round once, at the end (BR-50.10 rounding rule).
        raw = sum(parts.values(), Decimal(0)) * (Decimal(1) + OVERHEAD)
        out["costUsdMonth"] = str(raw.quantize(CENT, rounding=ROUND_HALF_UP))
        out["costPriced"] = sorted(parts)
    return out


def main(argv: list[str] | None = None) -> int:
    d = Inputs()
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--users", type=int, default=d.users)
    p.add_argument("--think-time-s", type=int, default=d.think_time_s)
    p.add_argument("--journal-lines-per-hour", type=int, default=d.journal_lines_per_hour)
    p.add_argument("--journal-target-lines-per-s", type=int, default=d.journal_target_lines_per_s)
    p.add_argument("--order-lines-per-hour", type=int, default=d.order_lines_per_hour)
    p.add_argument("--scanners", type=int, default=d.scanners)
    p.add_argument("--scan-interval-s", type=int, default=d.scan_interval_s)
    p.add_argument("--years", type=int, default=d.years)
    p.add_argument("--ledger-gb-per-year", type=int, default=d.ledger_gb_per_year)
    p.add_argument("--orders-gb-per-year", type=int, default=d.orders_gb_per_year)
    p.add_argument("--scans-gb-per-year", type=int, default=d.scans_gb_per_year)
    p.add_argument("--peak-kafka-msgs-per-s", type=int, default=d.peak_kafka_msgs_per_s)
    p.add_argument("--consumer-parallelism", type=int, default=d.consumer_parallelism)
    p.add_argument("--node-usd-month", default=None, help="provider rate per node per month, USD")
    p.add_argument("--pg-gib-usd-month", default=None, help="provider rate per GiB-month of Postgres volume, USD")
    p.add_argument("--broker-usd-month", default=None, help="provider rate per Kafka broker per month, USD")
    a = p.parse_args(argv)
    try:
        inp = Inputs(
            users=a.users, think_time_s=a.think_time_s, journal_lines_per_hour=a.journal_lines_per_hour,
            journal_target_lines_per_s=a.journal_target_lines_per_s, order_lines_per_hour=a.order_lines_per_hour,
            scanners=a.scanners, scan_interval_s=a.scan_interval_s, years=a.years,
            ledger_gb_per_year=a.ledger_gb_per_year, orders_gb_per_year=a.orders_gb_per_year,
            scans_gb_per_year=a.scans_gb_per_year, peak_kafka_msgs_per_s=a.peak_kafka_msgs_per_s,
            consumer_parallelism=a.consumer_parallelism,
        )
        rates = [None if v is None else Decimal(v) for v in (a.node_usd_month, a.pg_gib_usd_month, a.broker_usd_month)]
        result = estimate(inp, *rates)
    except (CapacityError, ArithmeticError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())

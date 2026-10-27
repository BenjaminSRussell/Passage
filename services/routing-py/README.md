# routing-py: transportation route optimizer (wave 34, PSG-34)

Python numeric service for wave 34. It solves capacitated vehicle routing with time windows
using Google OR-Tools (`RoutingModel`). The transport service (`services/transport`) owns
loads, rates and the API; this service only solves routes.

## Layout

```
tms_optimizer/vrp.py   solve(Problem) -> Solution; pure function, no I/O
tests/test_vrp.py      feasibility tests against testdata/small-6.json and random instances
testdata/small-6.json  hand-checkable fixture: 6 stops (2,100 kg), 2 vehicles of 1,100 kg, 1 km = 1 minute
pyproject.toml         Python 3.11+, ortools, pytest and hypothesis (dev)
```

## Model (BR-34.1, slice 1 subset)

- Capacity dimensions: weight kg, volume litres, pallets (hard), stops per route (max 25).
- Time windows per stop: hard lower bound at `earliest`; soft upper bound at `latest` with
  `lateness_per_min` (default 50) penalty; hard bound at `latest + 60` min.
- Maximum route duration per vehicle (`max_route_min`, default 11 h).
- Objective (BR-34.1 item 9), all in money minor units so every term is on one scale:
  distance cost (`cost_per_km`, default 1) + time cost (`cost_per_hour`, default 0) +
  fixed vehicle cost + lateness penalties.
- Stops that cannot be served are dropped with penalty 10,000 (money units) and returned
  in `dropped`. They are never silently discarded, and a feasible stop is never dropped
  merely because it is far away.
- Search: `PARALLEL_CHEAPEST_INSERTION` first solution, `GUIDED_LOCAL_SEARCH`, time limit
  `time_limit_s` (default 60).

Travel time is `ceil(distance_km / speed_kmh × 60)` minutes on straight-line distance.
OSRM road distances are not used yet.

## Run tests

```sh
cd services/routing-py
python -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
pytest
```

The wave build pipeline has no Python check kind yet, so CI must run `pytest` here
(see NOT-IMPLEMENTED.md).

## Error and failure modes

| Condition | Behaviour |
|---|---|
| No vehicles | `ValueError("at least one vehicle is required")` |
| Solver finds nothing within the limit | `NoSolutionError`; the caller must retry or raise the limit |
| Unreachable stop | listed in `Solution.dropped`; the transport service reports TMS-0104 |

## Dependencies

`ortools>=9.10` (Apache-2.0). Test-only: `pytest`, `hypothesis`. No other runtime dependencies.
No HTTP layer in this slice; see NOT-IMPLEMENTED.md.

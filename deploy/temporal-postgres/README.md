# temporal-postgres

Single-replica Postgres for Temporal. The StatefulSet `serviceName` is `temporal-postgres-rw`, the ClusterIP Service clients use. Both `serviceName` and `clusterIP` are immutable, so a headless Service for stable pod DNS needs a planned recreate of the StatefulSet (delete with `--cascade=orphan`, then sync) and of the Service; do not change them in a normal sync.

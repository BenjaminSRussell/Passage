# Runbook: Postgres replication lag

Alert: `PassageReplicationLagHigh` (OPS-1708, lag > 60 s for 5 min). SLO for the passive region: p95 < 30 s.

1. Check the source: `kubectl cnpg status passage-pg -n passage-data`. A lagging standby with a healthy
   primary usually means network or WAL apply.
2. Check WAL volume on the primary (peak budget 40 MB/s, PRD 9.50.19). A spike from a bulk job
   (month-end, load campaign) is expected; confirm the job is still making progress.
3. Check the cross-region link and the replication user certificate expiry (`passage-pg-replication-tls`).
4. If lag exceeds `FAILOVER_MAX_LAG_S` (300 s), failover is blocked (OPS-1701). Do not force it without
   a second approver.

# Runbook: certificate expiry

Scope: TLS certificates for the public API, the admin ports, Postgres replication (`passage-pg-replication-tls`),
the Strimzi cluster CA (`passage-cluster-ca-cert`) and its copy in the peer region
(`passage-kafka-remote-ca`), and Temporal mTLS. cert-manager renews the issued certificates and Strimzi
renews its own CA. The daily certificate expiry scan (9.50.16) alerts below 14 days and pages at 7 days
when a renewal has not happened; the scan job belongs to the ops service (`docs/ops/NOT-IMPLEMENTED.md`).
The MM2 SCRAM password (`passage-mm2-remote`) is not a certificate and does not expire.

## 1. Find what is expiring
1. Read the scan output or run `kubectl get certificates -A` and sort by `Not After`.
2. For each entry, check the issuing cluster issuer and the last `Ready` condition. A failed renewal
   names the reason in the `Certificate` status.

## 2. Automatic renewal failed
1. Check the issuer. An ACME or external CA outage is a dependency issue, not a Passage bug. Escalate to
   the platform owner and start step 3 in parallel.
2. Check DNS-01 or HTTP-01 challenge objects: `kubectl get challenges -A`. A stuck challenge usually means
   a DNS record or ingress route was changed.
3. Force one renewal: `cmctl renew <certificate> -n <namespace>`. Wait for `Ready=True`.

## 3. Manual fallback (renewal not possible before expiry)
1. Issue a short-lived certificate from the internal issuer with the same SANs. Store it in the same secret
   name so the services pick it up without a config change; services re-read certificate files (file watch).
2. Confirm the new not-after date. Record it in the change log.
3. Replace the external certificate as soon as the public issuer recovers.

## 4. Replication and broker certificates
Cross-region trust is pinned in `passage-kafka-remote-ca` (MM2) and the `ca.crt` key of
`passage-pg-replication-tls` (CNPG). When Strimzi renews the cluster CA, copy the new
`passage-cluster-ca-cert` to the peer region's `passage-kafka-remote-ca` before the old CA expires.
Rotating a CA is a two-step change: add the new CA to the trust secret, then rotate leaf certificates. Do
not remove the old CA until both regions trust the new one, or MM2 and CNPG replication stop.

## 5. Verify
1. `openssl s_client -connect <host>:443 -servername <host>` shows the new not-after date.
2. Replication lag returns to baseline (see the Postgres replication runbook).
3. MM2 connectors report RUNNING.

## Never
Disable certificate verification to get past an expiry. Services verify with `verify-full`.

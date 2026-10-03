# Smoke-test results

## Results (2026-10-03, against a real account and one of its zones)

### Hand-written read commands (`make smoke`)

255 commands, 509 runs (text + `--json`; `r2 get` text only):

| outcome | runs |
| --- | --- |
| PASS | 299 |
| EXPECTED-UNAVAILABLE | 210 |
| FAIL | 0 (7 before the fixes above) |

EXPECTED-UNAVAILABLE is mostly products not enabled on this account (Access,
Spectrum, Containers, Pipelines/K2 need Workers Paid, Agent Memory and
Artifacts are private beta, dispatch namespaces), user-level endpoints with an
account token (`user get/invites/memberships`), Page Rules (no account-owned
tokens), custom certificates (plan), and placeholder IDs for resources the
account doesn't have (KV, D1, queues, Hyperdrive, Vectorize, Pages, ...),
which must and do produce a friendly not-found. `containers images list`
needs a POST for a registry credential and is documented as refused in
read-only mode.

The first pass also failed `workers preview secret list` (needed
`--preview`; a script fix, the error was friendly) and `r2 get --json` (a
download writes the object's bytes; `--json` doesn't apply, now run in text
mode only).

### Generated GET sweep (`cfctl api smoke`)

917 operations selected; 895 ran, 22 skipped (a required query parameter
with no enum/default: free-text `query`, `resource_id`, `bucket`, ...).

| outcome | ops |
| --- | --- |
| ok (2xx) | 624 |
| expected-4xx | 269 (400: 37, 401: 56, 403: 158, 404: 15, 410: 2, 200 + success:false: 1) |
| 5xx | 2 |
| client-bug | 0 (6 before the fixes: raw JSON in 5 errors, 1 `HTTP 200` error) |
| rate-limited | 0 |
| skipped | 22 |

`--all --max-pages 3` re-runs on the 141 paginated operations that succeeded:
all 141 ok.

## Still failing, and why (Cloudflare-side)

- `GET /zones/{zone_id}/settings/zaraz/export`: 500 on every attempt (also via
  `cfctl api request` with no parameters). Zaraz isn't configured on the zone.
- `GET /accounts/{account_id}/ai-gateway/logging-state`: 503 "temporarily
  unavailable" (code 7015), every attempt over ~1 hour.
- Spec vs API mismatches seen in the sweep (not cfctl bugs; the generated
  commands send what the spec says): Radar endpoints require `dateRange`
  (or start/end) though the spec marks it optional (the sweep passes
  `dateRange=1d`); `radar-get-quality-speed-histogram`'s spec enum
  is lowercase (`bandwidth`) but the API wants `BANDWIDTH`; AI Gateway usage
  history needs `start_time`/`end_time` the spec doesn't require; several
  paths answer 400 "Could not route" / "No route for that URI" (code
  7000/7003): billing bad-debt/unpaid-invoices, magic redundancy groups,
  origin cloud regions, SSL recommendation, a few container/health routes;
  Intel endpoints need `ipv4`/`domain` the spec doesn't mark required.
- `ssl custom list` on a Free zone answers 400 "Plan level does not allow
  custom certificates" rather than an empty list (Cloudflare behavior).

## Follow-ups

All three follow-ups found by the smoke run were fixed in 0.2.006: empty
`r2 buckets list` columns, `zones list` missing IDs/plans, and the
unknown-tag-with-flags error message.

# momentarr

[More Drumandbytes projects](https://drumandbytes.com/projects/)

A caching, serialising proxy for [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr)-compatible
Cloudflare solvers ([Byparr](https://github.com/ThePhaseless/Byparr), FlareSolverr). It speaks the same
`/v1` API, so Prowlarr and friends point at momentarr instead of the solver.

## Why

A solver spins up a browser per challenge, and browsers are what OOM a small node. Two requests at
once means two browsers. Solving the same site again on every search also means the same browser
spike each time, even though the `cf_clearance` cookie from the last solve is still valid.

momentarr does two things in front of the solver:

- **Reuses the clearance.** `cf_clearance` is bound to the client IP and user agent, not to the
  browser. After a solve, momentarr keeps the cookies and user agent per host. The next request for
  that host is a plain HTTP fetch with them, taking about 100ms and no browser. If the site challenges
  anyway, the entry is dropped and the request goes to the solver.
- **Runs one solve at a time.** Everything that needs the solver waits for a slot
  (`BACKEND_CONCURRENCY`, default 1). After the wait it checks the cache again, so a burst of searches
  against one site costs a single solve.

Queue time counts against the request's `maxTimeout`, the same as a busy FlareSolverr. Set Prowlarr's
request timeout high enough to cover a solve plus whatever is ahead of it (90s works for one solver).

Measured against kickasstorrents.to and 1337x.to with Byparr as the backend:

| | Latency | Memory |
| --- | --- | --- |
| Solve (miss) | 6–13s | Byparr peak ~900MiB, idle ~100MiB |
| Cached clearance (hit) | 30–130ms | momentarr ~6MiB |

It must share the solver's egress IP. In Kubernetes, run both as containers in the same pod as the
consumer (e.g. behind a VPN sidecar).

## Configuration

| Env | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8191` | Listen port (FlareSolverr's) |
| `BACKEND_URL` | `http://127.0.0.1:8192` | The real solver |
| `BACKEND_CONCURRENCY` | `1` | Solver requests in flight |
| `COOKIE_DIR` | `/tmp/momentarr-cookies` | Clearance cache |
| `COOKIE_TTL_HOURS` | `720` | Upper bound; the cookie's own expiry wins if sooner. Sites set `cf_clearance` from 30 minutes (Cloudflare's default) up to a year (1337x); a revoked one is dropped as soon as the site challenges it |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

## Logs

The output is logfmt on stderr.

- `info` logs one line per request, saying whether it was served from cache, solved by the backend or
  passed through, with timings. Every 15 minutes it adds a `heartbeat` line with counters (cache hits,
  rejected clearances, solves, failures, queue timeouts).
- `warn` covers sites rejecting a clearance, queue timeouts, cache I/O and unparseable backend replies.
- `error` means the backend is unreachable or a forward failed. Backend reachability is logged when it
  changes, not on every beat.
- `debug` traces each request step by step.

Not cached: requests with `returnScreenshot`, and hosts whose solve returned no cookies. `sessions.*`
commands are passed through, through the same queue.

## Run

```bash
docker run -d --name byparr -e PORT=8192 -p 8191:8191 ghcr.io/thephaseless/byparr:latest
docker run -d --name momentarr --network container:byparr ghcr.io/drumandbytes/momentarr:latest
curl -s localhost:8191/v1 -d '{"cmd":"request.get","url":"https://example.com"}'
```

The image is `FROM scratch`: a static binary, CA certificates and `/tmp`, running as UID 1000. It works
with a read-only root filesystem as long as `/tmp` is writable.

## How it was made

momentarr was developed with an AI coding assistant (Claude). The design, review and testing are
mine, and it runs in my own homelab behind Prowlarr.

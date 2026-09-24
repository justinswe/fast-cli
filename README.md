# fast-cli

`fast` is a command-line reimplementation of [fast.com](https://fast.com)'s speed test in Go. It is not a generic
downloader that hits Netflix's servers: it reproduces fast.com's own methodology — the same token discovery, the same
targets API call, the same 2048-byte warm-up followed by 25 MB range requests, the same 1→3→5→8 connection ramp,
the same 150 ms sampling, the same `stableMovingAverage` aggregator and the same stop rule — so its numbers are meant
to be compared with what fast.com shows in a browser. The methodology is written down in
[docs/PROTOCOL.md](docs/PROTOCOL.md), which was reverse-engineered from the fast.com web-app bundle; `internal/engine`
implements it.

Standard library only; no third-party dependencies.

## Requirements

- Go 1.26 or newer to build.
- Outbound HTTPS to `fast.com`, `api.fast.com` and the Netflix Open Connect servers (`*.oca.nflxvideo.net`) the API
  hands out.

## Build

```
make build            # go build -o bin/fast ./cmd/fast
make                  # vet + test + build
go install ./cmd/fast # installs `fast` into $GOBIN
```

`fast` reports its version as `dev` unless built with `-ldflags "-X main.version=<v>"`.

## Usage

```
usage: fast [-u|--upload] [--json] [--single-line] [--timeout d]
```

| Flag | Meaning |
|---|---|
| `-u`, `--upload` | Also measure upload speed and loaded latency (fast.com "show more info"). Without it only download and latency run. |
| `--json` | Print one JSON object on stdout at the end instead of live output (schema below). |
| `--single-line` | Print the final summary on a single line and omit the `Client`/`Server(s)` lines. Live output is unaffected. |
| `--timeout d` | Abort the whole run after `d` (a Go duration such as `45s` or `2m`). `0` = no cap (default). |

Flags follow Go's `flag` conventions, so `-json` and `--json` are equivalent. Positional arguments are rejected.

On a terminal a single progress line is redrawn in place while the test runs (`Download: 245 Mbps   Latency: loaded
45 ms`, then `Upload: …` when `-u` is on). When stdout is not a terminal the progress line is printed
newline-terminated at most once per second. Ctrl-C stops the run.

### Sample output

Default run (download, then unloaded latency). Example output; your numbers will differ:

```
$ fast
Download: 250 Mbps
Latency: unloaded 12 ms, loaded 45 ms
Client      The Dalles, US   34.19.110.48   Google LLC
Server(s)   Seattle, US  |  San Jose, US
```

<!-- TODO(docs): replace both samples with real output from bin/fast once the engine lands. -->

With upload (`-u`): the loaded latency measured during the upload phase is shown alongside the download one — fast.com
measures it too but hides it behind an advanced setting:

```
$ fast -u
Download: 250 Mbps
Latency: unloaded 12 ms, loaded 45 ms (download), 60 ms (upload)
Upload: 12 Mbps
Client      The Dalles, US   34.19.110.48   Google LLC
Server(s)   Seattle, US  |  San Jose, US
```

Speeds and latencies in the human output use fast.com's display rounding (see Methodology). The `Client` line is
location, public IP and ISP as reported by the fast.com API (ISP may be absent); `Server(s)` lists the distinct
locations of the first three targets, as fast.com does.

### `--json` schema

`--json` suppresses live output and prints exactly one newline-terminated JSON object on stdout. Speeds are raw
megabits per second (bits/s ÷ 1e6, not fast.com-rounded); latencies are raw milliseconds; durations are seconds.

```json
{
  "version": "dev",
  "timestamp": "2026-09-17T21:04:33Z",
  "download_mbps": 251.37,
  "upload_mbps": 12.41,
  "latency_ms": {
    "unloaded": 12.3,
    "loaded_download": 45.1,
    "loaded_upload": 60.2
  },
  "client": {
    "ip": "34.19.110.48",
    "asn": "396982",
    "isp": "Google LLC",
    "city": "The Dalles",
    "country": "US"
  },
  "servers": [
    {
      "name": "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=…&t=…",
      "url": "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=…&t=…",
      "city": "Seattle",
      "country": "US"
    }
  ],
  "durations": {
    "download_seconds": 7.35,
    "upload_seconds": 8.1
  }
}
```

| Key | Type | Notes |
|---|---|---|
| `version` | string | Build version (`dev` unless set with `-ldflags`). |
| `timestamp` | string | RFC 3339 time at which the result was written. |
| `download_mbps` | number | Download speed, Mbit/s. |
| `upload_mbps` | number or `null` | Upload speed, Mbit/s; `null` when `-u` was not given. |
| `latency_ms.unloaded` | number | Unloaded latency (idle-line phase), ms. |
| `latency_ms.loaded_download` | number | Loaded latency measured during the download phase, ms. |
| `latency_ms.loaded_upload` | number or `null` | Loaded latency during upload; `null` without `-u`. |
| `client.ip` | string | Public IP as seen by the fast.com API. |
| `client.asn` | string | Autonomous system number (string even if the API sends a number). |
| `client.isp` | string | ISP name; empty string if the API omits it. |
| `client.city`, `client.country` | string | Client location. |
| `servers[]` | array | The targets returned by the API, in API order. |
| `servers[].name`, `servers[].url` | string | Target URL as returned by the API (the two are identical in practice). |
| `servers[].city`, `servers[].country` | string | Target location. |
| `durations.download_seconds` | number | Wall-clock length of the download phase. |
| `durations.upload_seconds` | number or `null` | Wall-clock length of the upload phase; `null` without `-u`. |

## Methodology

A summary of [docs/PROTOCOL.md](docs/PROTOCOL.md); section letters refer to that document.

- **Token (a).** `GET https://fast.com/`, find the `app-<hash>.js` bundle, extract `token:"…"`. If scraping fails the
  long-stable value `YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm` is used.
- **Targets (b).** One `GET https://api.fast.com/netflix/speedtest/v2?https=true&token=…&urlCount=5` per run. The
  response carries the client's IP/ASN/ISP/location and five target URLs. Each target URL is turned into a range
  template by replacing `speedtest` with `speedtest/range/`; the same five targets are reused for every phase.
- **Workers (c).** Each connection ("worker") loops over the targets round-robin and issues back-to-back requests.
  The first request on a worker is `GET …/speedtest/range/0-2048` (2,049 bytes — a warm-up), then
  `…/range/0-26214400`, `…/range/0-26214399`, … (25 MiB each, upper bound decremented per completed request to the
  same host).
- **Connection ramp (c).** A phase starts with 1 worker. Every 150 ms tick, after computing speed: `> 50 Mbit/s → 8`
  workers, `> 10 Mbit/s → 5`, `> 1 Mbit/s → 3`, and with fewer than 2 workers anything `> 0.5 Mbit/s → 3`. Workers
  are never removed; the cap is 8.
- **Sampling (d).** Every 150 ms the new byte/time intervals from all workers are merged into one snapshot whose
  time is the **union of the connections' busy intervals** (overlaps counted once, idle gaps excluded). Speed is
  therefore goodput during busy time, not bytes ÷ wall-clock. A tick without new data from every worker produces no
  snapshot.
- **Aggregation (d).** `stableMovingAverage(windowSize = 5)`: while the speed of the last five snapshots is
  non-decreasing, report that windowed speed; the first time it drops, freeze the window that produced the peak as the
  start point and from then on report the cumulative average from that start to now. This is the number displayed
  and the number in the result — not a percentile, not the last window.
- **Stop rule (d).** `stableDeltaMeasurementsStopper`: at each tick with a snapshot, once at least 7 s have elapsed
  since the first target URL arrived, stop if the last six recorded aggregate speeds are all within ±2 % of the
  current aggregate and the aggregate has not risen over the last three ticks. Hard stop at 30 s regardless.
- **Latency measurement (e).** A ping is `POST …/speedtest/range/0-0` with an empty body; the sample is time to
  first byte on a warm connection (fast.com: Resource Timing `responseStart − requestStart`; here: `httptrace`
  `WroteRequest → GotFirstResponseByte`).
- **Unloaded latency (e).** A dedicated phase after download: 5 workers ping back-to-back with no data transfer.
  Every 150 ms take the 10th percentile per worker and the minimum across workers; done when more than 5 s have
  passed and more than 50 samples exist.
- **Loaded latency (e).** During download and upload every worker pings about once per second (next ping
  `max(1000 − latency, 0)` ms after the previous). The value is the minimum over workers of the 75th percentile,
  considering only workers with more than 4 samples (all workers if none has).
- **Upload (f).** Same URL rewriting, ramp, sampling, aggregator and stop rule as download, using `POST` with a
  pseudo-random ASCII body of the request size (`Content-Type: application/octet-stream`); the server answers `200`
  with an empty body. First request 2,048 bytes, then `26214400 − k` bytes.
- **Phase order (g).** download → unloaded latency → upload (only with `-u`; fast.com always runs it but shows the
  upload figure only under "show more info"). Loaded-latency pings run concurrently with the data workers in
  download and upload. Between phases all measurements are reset; targets are not refetched.
- **Failures (c).** A connection failure that delivered no bytes restarts the whole attempt with fresh state after
  `min(2^attempt, 200)` ms, marking the target bad (bad after 2 failures per host, or 3 per site); up to 10 attempts.
  Per request: 9 s until the first byte, 60 s overall.
- **Display (d, e).** Speeds are decimal (`1e6`, never `1024`): `< 1 Mbps → Kbps`, `≥ 995 Mbps → Gbps`; one decimal
  below 9.95, whole numbers below 100, nearest 10 above. Latency: whole ms below 1 s, one decimal of seconds above.
- **Headers (h).** No explicit request headers other than `Content-Type` on non-empty uploads. No cookies. No
  telemetry (fast.com's `ichnaea` posts are not replicated).

## How close to fast.com is this?

The algorithm, constants and phase order are fast.com's. The transport is not a browser, so some measured
quantities differ by construction. Known, deliberate differences:

- **HTTP stack.** fast.com runs on the browser's fetch/XHR stack; `fast` uses Go's `net/http` with one shared
  `http.Transport`, keep-alives on. Connection pooling, TLS implementation, TCP socket options and receive buffering
  are Go's, not Chrome's or Firefox's.
- **HTTP version.** <!-- TODO(engine): copy the engine's HTTP/2 negotiation comment here. -->
- **Progress granularity.** A browser emits `progress` events on its own schedule; `fast` records an interval per
  `Read` from the response body. The union-of-busy-intervals merge makes the result largely insensitive to this, but
  the raw intervals are not identical.
- **Latency source.** fast.com reads `responseStart − requestStart` from the Resource Timing entry of each ping and,
  when the Resource Timing buffer is full, silently falls back to half the wall-clock round trip. `fast` always uses
  `httptrace` (`WroteRequest → GotFirstResponseByte`) and never halves.
- **Upload byte counting.** fast.com counts bytes reported by `xhr.upload.onprogress` — bytes handed to the browser's
  network stack, not bytes acknowledged by the server. `fast` counts bytes written to the socket, which has the same
  early-overshoot bias but not the same buffer sizes.
- **User-Agent.** Browsers always send one; fast.com sets none explicitly. `fast` sends `fast-cli/<version>`.
- **JS quirks deliberately not replicated** (numbered as in PROTOCOL.md §4): the snapshot early-return that discards
  carried-over intervals (§4.2); `percentile` returning `NaN` for one sample and `undefined` for none (§4.3 — `fast`
  returns the single value and skips empty workers); the unloaded-latency phase having no maximum duration (§4.4 —
  `fast` caps it); the Resource Timing fallback (§4.5). <!-- TODO(engine): confirm each against the engine code. -->
- **JS quirks replicated on purpose:** the 2,048-byte first request (§4.1), the always-ranged URL form (§4.7),
  `stable` always true (§4.9). <!-- TODO(engine): confirm. -->
- **Rounding.** Human output applies fast.com's display rounding; `--json` gives raw values, so a JSON
  `download_mbps` of `251.37` corresponds to a fast.com display of `250 Mbps`.

To compare: run `fast -u` and fast.com (with "show more info") back-to-back on the same machine and network, ideally
several times each. Two consecutive runs of fast.com itself typically differ by a few percent; differences within
that band are noise, not methodology. Note that the API may hand out a different set of five targets to each run.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | The test failed (network/API error, no targets, or `--timeout` elapsed). The reason is printed to stderr. |
| 2 | Usage error (unknown flag or unexpected positional argument). |
| 130 | Interrupted by Ctrl-C or SIGTERM. |

## License

No license has been chosen for this repository yet.

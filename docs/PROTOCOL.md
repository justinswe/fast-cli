# fast.com speed-test protocol — implementation-grade specification

This document is the methodology reference for fast-cli. It describes, constant by constant, what the fast.com web
app does when it runs a speed test: token discovery, the targets API, the download/upload/latency phases, the
throughput aggregation and stop rule, and the display conversion. `internal/engine` implements it; `internal/fastcom`
implements sections a–b; `internal/output` implements the display conversion in section d. Where the JS has bugs or
ambiguities they are listed in §4 with a recommendation for native ports; the README records which of those fast-cli follows.

Reverse-engineered from the live bundle `https://fast.com/app-0bffe1.js` (133,839 bytes, fetched 2026-09-17).
Line citations are written as `L<n>`; line numbers refer to the prettier-formatted bundle.
The bundle is a browserify bundle; module numbers below are the browserify module ids:

| id | module | lines |
|---|---|---|
| 1 | `./aggregator/movingAverage` (NOT used by fast.com; default only) | L40–57 |
| 2 | `./aggregator/stableMovingAverage` (**used**) | L60–111 |
| 3 | app bootstrap (`tester` construction, token, endpoint, phase kickoff) | L113–233 |
| 4 | `./config` (defaults + validators) | L236–415 |
| 8 | `./requester/xhr` (all HTTP I/O, timing) | L706–1030 |
| 9 | `./snapshot` (multi-connection byte/time merging) | L1032–1226 |
| 10 | `./stopper/stableDeltaMeasurementsStopper` (**used**) | L1228–1320 |
| 11 | `./stopper/stableMeasurementsStopper` (NOT used; default only) | L1322–1404 |
| 12 | `./tester` (test engine) | L1406–2090 |
| 13 | `./timer` | L2095–2112 |
| 14 | `./ui` (unit conversion, phase sequencing) | L2116–3043 |
| 15 | `./url_getter` (API fetch, target rotation, bad-URL bookkeeping) | L3045–3312 |
| 16 | `./utils` (`percentile`, `genBlob`, XHR factory) | L3314–3470 |

Conventions: all engine times are **milliseconds (fractional)** from `performance.now()`; all speeds inside the
engine are **bits per second**; byte counts are raw bytes. `1e3`/`1e6` (decimal) everywhere — never 1024.

---

## a. Token discovery

1. `GET https://fast.com/` (HTML, 26,326 bytes). The app bundle is referenced once, as a root-relative script
   tag with a content hash in the file name (landing page as fetched 2026-09-17, line 332):
   ```html
   <script src="/app-0bffe1.js"></script>
   ```
   Discovery regex: `<script src="(/app-[0-9a-f]+\.js)">` → fetch `https://fast.com` + capture group.
2. The token is a **hard-coded string literal** in the bundle, present twice with the identical value
   (`token:"YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm"`):
   - app config `getTestOcasParams.token` (L183) — this is the one actually sent;
   - `url_getter` `DEFAULT_PARAMS.token` (L3107) — fallback used only when the caller omits `token`.
   Extraction regex on the JS: `token:"([A-Za-z0-9+/=]+)"`. It is base64 of `asdfasdlfnsdafhasdfhkalf`
   and has been stable for years; a client may hard-code it as the fallback default when scraping fails.
3. Other `DEFAULT_PARAMS` (L3105–3111): `https: true`, `urlCount: 3`, `endpoint: "api-global.netflix.com/oca/speedtest"`,
   `extraParams: {}`. fast.com overrides `endpoint` and `urlCount` (see b). The legacy endpoint is only a fallback.

## b. Target API

### Request
`formatUrl` (L3066–3086) builds, in this exact order:
```
"https://" + endpoint + "?https=" + https + "&token=" + token + "&urlCount=" + urlCount   [+ "&k=v" per extraParams]
```
With the app's `getTestOcasParams` (L180–185): `https: !0` (JS `true` → stringifies as `true`),
`endpoint: "api.fast.com/netflix/speedtest/v2"` (L169), `token` above, `urlCount: 5`, no `extraParams`:

```
GET https://api.fast.com/netflix/speedtest/v2?https=true&token=YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm&urlCount=5
```
Sent by `url_getter.refresh` (L3262–3285) through the same XHR requester as downloads:
`req.start(url, dummy, urlSuccess, true)` → `XMLHttpRequest.open("GET", url, true)`, **no custom headers**,
`withCredentials` never set (cookies not sent; the `Set-Cookie: nfvdid=...; Domain=.netflix.com` in the
response is ignored), default `responseType` (text), `xhr.timeout = 60000` ms plus a 9,000 ms watchdog
(see h). Query values are not URL-encoded (they are plain ASCII).

### Response schema (observed in a live API response, 2026-09-17)
```json
{
  "client": {
    "ip": "34.19.110.48",
    "asn": "396982",
    "isp": "<optional; ABSENT in this sample>",
    "location": { "city": "The Dalles", "country": "US" }
  },
  "targets": [
    {
      "name": "https://ipv4-c204-sea001-ix.1.oca.nflxvideo.net/speedtest?c=us&n=396982&v=319&e=1789680514&t=xpy_t0pAjNq4NhVjI-jHrFpBioo04qSZI7vTsA",
      "url":  "<identical to name>",
      "location": { "city": "Seattle", "country": "US" }
    }
    /* exactly urlCount (=5) entries */
  ]
}
```
- `client.isp` may be missing; UI does `client.isp ? client.isp.replace(/_/g," ") : ""` (L2604–2610).
- `targets[].url` query: `c` (client country), `n` (ASN), `v` (weight/version), `e` (unix expiry, here ≈ 6 h ahead), `t` (signature).
  The `e`/`t` parameters mean target URLs expire; fast.com fetches once per test run and reuses the list across phases (below).
- Response headers observed: `Content-Type: application/json`, `Cache-Control: no-cache, no-store`.

### URL template transform (`parseSpeedTestUrls`, L3047–3065)
For each target: `url = target.url.replace(/speedtest/, "speedtest/range/")` (first occurrence only) →
```
https://<oca-host>/speedtest/range/?c=us&n=396982&v=319&e=...&t=...
```
This "range template" is what the engine stores and later rewrites by string-replacing the literal `"/range/"`
(see c/e/f). Targets whose template `isBadUrl()` is true are skipped; **if that removes every target, all of
them are used anyway** (L3064). `clientInfo = response.client` with `clientInfo.servers = [kept targets]`.

### How many, when (re)fetched, rotation (`getNext`, L3207–3250; `get`, L3183–3206)
- `urlGetter.reset()` (empties the list) is called at the start of **download** (L2011) and at the start of a
  latency/upload phase **only if that phase has already run once** in this page (L2021, L2033). So the normal
  run download → latency → upload performs **one API call**, and latency/upload reuse the same 5 targets.
- `getNext` refetches when `testUrls.length < 3 || refresh` (L3236). Otherwise it returns
  `testUrls[curUrlInd = (curUrlInd + 1) % testUrls.length]`. After a refresh `curUrlInd = 0` and the caller
  that triggered the refresh gets `testUrls[0]`; callers arriving during an in-flight refresh poll every
  50 ms (`waitTillRefresh`, L3219–3225) and then take the next index. Net effect: worker *k* (0-based) of a
  phase uses target `k mod 5`, continuing the rotation across phases (the index is not reset between phases).
- `getNext` is called exactly once per `startNewWorker()` (L1896–1906).

### Bad-URL bookkeeping (`urlReporter`, L3112–3181) — `ocaFailures` semantics
`parseUrl(url)` (L3114–3133): `normalizedUrl` = scheme+host+path up to and including `/speedtest` + `?` + query
(i.e. the `/range/...` part is stripped); `oca` = hostname; `site` = `hostname.split(".")[2]`.
**Quirk:** for current hostnames (`ipv4-c204-sea001-ix.1.oca.nflxvideo.net`) `site` is always the literal `"oca"`,
so `siteFailures` is effectively one global counter.

- `reportBadUrl(url)` (L3160–3178): `badUrls[normalized] = true`; `ocaFailures[oca] += 1`; **if that was the
  OCA's first failure** (`=== 1`) also `siteFailures[site] += 1`.
- `isBadUrl(url)` (L3145–3159): `badUrls[normalized]` set **or** `ocaFailures[oca] >= 2` **or** `siteFailures[site] >= 3`.
- `urlGetter.reportBadUrl` (L3251–3261) then filters `testUrls` to the non-bad ones. When fewer than 3 remain the
  next `getNext` refetches from the API (bad targets in the fresh list are filtered again, with the all-bad fallback).
- Who calls it: `tester.test(attempt+1, error)` (L1930) calls `urlGetter.reportBadUrl(error.url)` when the error
  has a `.url`, which is only true for `DownloadOcaDataError` raised by a **download/upload connection failure**
  (L1645–1656). API errors (`GetOcaUrlError`, `NoOcaUrlsError`) restart the attempt but do not mark URLs.
  Latency-ping failures never restart nor mark anything.
- The whole reporter is reset only by `urlGetter.reset()` (see above).

## c. Download phase

### Worker start (`startTestWorker`, L1525–1768)
Per worker: `req = requester()` (data connection), `pinger = requester()` (latency pings), `requestSize =
config.firstRequestBytes = 2048` (L1744), `oca = url.split("/")[2]` (hostname).

**Critical quirk — `supportsProgress()` (L1003–1010) returns `undefined` on its first call** on each fresh
requester instance (it assigns the flag but has no `return` after the assignment; verified in the raw
minified source). Consequences, in order:
1. L1750 `req.supportsProgress() && (requestSize = MAX_PAYLOAD_BYTES)` → **falsy, so the first request stays 2048 bytes**.
2. Every later call returns `true` in any modern browser.

### Range URL construction (`sendDownloadRequest`, L1531–1565)
```
if (testAttempt < 3
    || config.maxBytesInFlight - 1 < activeWorkersCount * maxPayloadSize
    || !req.supportsProgress())
      rangeUrl = template.replace("/range/", "/range/0-" + requestSize)      // ranged
else  { rangeUrl = template.replace("/range/", ""); requestSize = MAX_PAYLOAD_BYTES }  // unranged
```
- Ranged form: `https://<oca>/speedtest/range/0-2048?c=..&n=..&v=..&e=..&t=..`
- Unranged form: `https://<oca>/speedtest?c=..&...` (the whole 26,214,400-byte object).
- `MAX_PAYLOAD_BYTES = 26214400` (L1407) = 25 MiB. `maxBytesInFlight = 78643200` (L381) = 3 × 25 MiB, so the middle
  condition is true iff `activeWorkersCount >= 3`. Therefore the **unranged URL is only ever used on attempt ≥ 3
  while ≤ 2 workers are active**. On attempts 1–2 (the normal case) every request is ranged.
- **Server semantics (verified live):** `/range/0-N` returns `min(N+1, 26214400)` bytes (`0-2048` → `Content-Length: 2049`;
  `0-26214400` → `26214400`). `Content-Type: application/octet-stream`, `Cache-Control: no-store`,
  `Access-Control-Allow-Origin: *`, `Timing-Allow-Origin: *`, `Connection: keep-alive`, `Accept-Ranges: bytes`.

### Request mechanics (`startTest`, L759–860)
- `XMLHttpRequest`, `open("GET", rangeUrl, true)`, `overrideMimeType("text/plain; charset=x-user-defined")`,
  `responseType = "blob"`, `xhr.timeout = 60000`; `send()` deferred with `setTimeout(…, 0)`.
- Watchdog: `setTimeout(ontimeout, 9000)` armed at start; **cleared by the first `progress` event and never
  re-armed** (L813–814, L800). On firing: abort + complete `{success:false, response:{type:"Timeout"}}`.
- `start` timestamp: `lastProgressTime = lastCompleteTime = timer()` taken synchronously before the deferred `send()`.

### What is counted as bytes (`onprogress`, L813–828; `onload`, L795–812)
- Each `progress` event with `status ∈ {200, 304}` **and** `e.lengthComputable` yields one measurement
  `{bytes: e.loaded − lastLoaded, start: lastProgressTime, end: now}`; then `lastProgressTime = now`.
  (Progress before headers/with other statuses is ignored. `lengthComputable` requires `Content-Length`, which the OCA sends.)
- On `readyState === 4` with `status ∈ {200, 304}`: one final measurement
  `{bytes: blob.size − lastLoaded, start: lastProgressTime, end: now}` (normally 0 bytes), then completion
  `{success:true, start: requestStartTime, end: now}` enriched with Resource Timing (`extractTimingInfo`, L727–745).
  Any other terminal status → `{success:false, response:{type:"RequestError", status, …}}`.
- Tester side (`onProgress`, L1603–1626): only `progress.success` measurements are kept; stored as
  `{bytes, time: end−start, start, end}` in `workerMeasurements[workerId]`; `bytes` undefined → substituted by
  `requestSize` (defensive; not hit with blob responses). Phase byte counter `stats[type].bytes[last] += bytes`.

### Request completion, size growth, "rangeFinished" (`completeFunc`, L1627–1691)
```
ocaRequest      = ocaCount[oca] || 0;  ocaCount[oca] = ocaRequest + 1        // completed requests to this host, this attempt
maxPayloadSize  = MAX_PAYLOAD_BYTES - ocaRequest                              // 26214400, 26214399, 26214398, ...
partialSuccess  = !data.success && workerMeasurements[workerId].length > 0    // failed but some bytes arrived
if (!data.success && !partialSuccess) → CONNECTION_FAIL, error.url = template → test(attempt + 1, error)   // restart whole attempt
else:
  if (req.supportsProgress())            // true on every call after the first → modern browsers
      requestSize = maxPayloadSize
  else                                   // legacy path (no progress events); NOT taken by modern browsers
      requestTime  = data.end - data.start
      requestSize  = min( floor(requestTimeMs * requestSize / requestTime),
                          5 * requestSize,
                          maxPayloadSize,
                          round(maxBytesInFlight / connections.max) )       // 78643200/8 = 9830400
      requestTimeMs = min(requestTimeMs + rangeRequestIncreaseMs(200), maxRangeRequestTimeMs(1000))   // starts at 300
      requestSize  = max(requestSize, 1024)
  sendRequest(...)                       // immediately issue the next range request on this worker
```
So in practice each worker does: `GET /range/0-2048` (2,049 B) → `GET /range/0-26214400` → `GET /range/0-26214399` → …
back-to-back until the phase stops. The decrementing upper bound is the only thing that varies the URL between
requests (cache-busting; `Cache-Control: no-store` anyway). **`rangeRequestIncreaseMs`, `startRangeRequestTimeMs`,
`maxRangeRequestTimeMs`, `maxRangeRequestTimeMs` and the 5×/1024 growth rule are dead code for modern browsers.**

`rangeFinished` (L1141–1188) is unrelated to HTTP ranges: it is the loop counter in `snapshot.makeSnapshot` that
counts how many per-worker measurement lists have been fully merged (see d).

### Concurrency (`reportMetrics` tail, L1850–1868; `test.startTest`, L1909–1921)
- Attempt start: `connections.min = 1` worker (L176, L1915).
- Every 150 ms tick, **after** speed computation, if `activeWorkersCount < connections.max (8)`:
  ```
  desired = active
  if      (speed > 5e7)                 desired = 8          // > 50 Mbit/s
  else if (speed > 1e7 && active < 5)   desired = 5          // > 10 Mbit/s
  else if (speed > 1e6 && active < 3)   desired = 3          // >  1 Mbit/s
  if      (speed > 5e5 && active < 2)   desired = 3          // > 0.5 Mbit/s  (evaluated AFTER the chain; overrides it)
  start (desired − active) new workers
  ```
  `speed` is the aggregated speed of this tick (undefined → no scaling if the tick produced no active snapshot).
  With 1 worker the last line wins: any speed > 0.5 Mbit/s jumps to 3 workers; a later tick can go 3→5 or 3→8, 5→8.
- **Connections are never removed**; `activeWorkersCount` only increases (there is no `minConnections`-based shrink).
- Scaling code runs identically for download and upload; not for the latency phase.

### Failure handling (`test`, L1908–1937)
- A non-partial connection failure (or API failure) restarts the **entire attempt**: `reset()` stops every
  requester and pinger, clears `workerMeasurements`, `latencyMeasurements`, `progressMeasurements`, snapshots,
  `ocaCount`, `downloadStartTime`, `requestTimeMs`; `testAttempt += 1`; bad URL reported; new attempt starts
  after `min(2^attempt, 200)` ms (L1934) with `connections.min` workers. `testAttempt > maxAttempts (10)` → END with
  `result: "fail"`. The phase byte counter is **not** reset between attempts.
- `testAttempt` also changes URL shape (unranged allowed from attempt 3, see above).

## d. Throughput computation

### Sampling
`reportMetrics(150)` (L1769–1880) runs on a `setTimeout` chain every `progressFrequencyMs = 150` ms (L187;
default would be 200). Elapsed time for all decisions:
```
testTime = (now − (downloadStartTime || startTime)) / 1000     // seconds
```
`downloadStartTime` is set when the **first worker receives its target URL** (L1889), i.e. API latency is excluded.

### Snapshot per tick (`snapshot.compute`, module 9)
Purpose: turn per-connection progress intervals into one `{bytes, time}` sample where `time` is the **union of
busy intervals across all connections** (overlaps counted once, idle gaps excluded). Algorithm, faithful to the JS:

1. `getLatestSnapshotMeasurements` (L1036–1086): for every key of `workerMeasurements` (ascending id order):
   `new = measurements[w][lastProcessed[w]:]`, `carry = overflow[w]`. **If any worker has neither new nor carried
   measurements the function returns `undefined` and no snapshot is produced this tick** (L1050–1062).
   Otherwise `items[w] = carry ++ new`, `overflow[w] = []`, `lastProcessed[w] = len(measurements[w])`, and
   `snapshotEnd = min over w of measurements[w][last].end` (the last **raw** measurement, even if already processed).
   *JS bug:* on the early return, `overflow[]` of workers already iterated has been cleared and is lost (L1063).
2. `trimSnapshotMeasurements` (L1087–1135): per worker, pop trailing items with `start >= snapshotEnd` into
   `overflow[w]`; if the last remaining item has `end > snapshotEnd`, split it proportionally:
   `ovBytes = m.bytes × (m.end − snapshotEnd) / (m.end − m.start)`; push `{start: snapshotEnd, end: m.end, bytes: ovBytes}`
   to `overflow[w]`; `m.bytes −= ovBytes; m.end = snapshotEnd` (mutates the stored object).
3. `makeSnapshot` (L1136–1197): merge all workers' items in ascending `start` (k-way merge of per-worker
   chronological lists), with `bytes = 0, time = 0, cur = firstStart`:
   `cur = max(cur, m.start); if (m.end > cur) { time += m.end − cur; cur = m.end }; bytes += m.bytes`.
   Result `{bytes, time, start: firstStart, end: cur}`, appended to `snapshots[]`.
4. Tick logic (L1799–1812): `activeSnapshot = snapshot.end − snapshot.start > 0`. Only then:
   `currentSpeed = aggregator(snapshots)` and the stopper is consulted.

Because `time` is the union of intervals whose `start` is the previous progress event (or request start), the
first-byte wait of each request is counted as busy time for that connection, but is masked whenever another
connection is transferring.

### Aggregator — `stableMovingAverage(windowSize = 5)` (L60–111; instantiated L156)
Returns **bits/s**: `1000 × bytes × 8 / times` with `times` in ms. Persistent state across ticks:
`startInd = 0, curSpeed = 0, bytes = 0, times = 0, fixed = false`; `snapshotResetThreshold = 5`.
```
aggregate(snapshots):
  n = len(snapshots); if (n < 5) reset()
  if (!fixed):
     W = last 5 snapshots; ws = ΣW.bytes / ΣW.time  (0 if ΣW.time == 0)          // bytes per ms
     if (ws >= curSpeed): startInd = n; curSpeed = ws; bytes = ΣW.bytes; times = ΣW.time
     else:                fixed = true                                            // windowed speed dropped for the first time
  for i in startInd .. n-1: bytes += s[i].bytes; times += s[i].time
  startInd = n
  return times > 0 ? 1000 * bytes * 8 / times : 0
```
Semantics: while the 5-snapshot windowed speed is non-decreasing (ramp-up / slow start) the reported speed is the
last-5-window speed. The first time the window speed falls, the window that produced the previous peak becomes the
fixed starting point and from then on the value is the **cumulative average from that window's start to now**.
Since `n < 5` resets the state, and each phase/attempt starts with `snapshot.reset()`, one shared aggregator
instance serves download and upload. (`windowSize` and `movingAverage(10)` of module 1 are unused by fast.com.)

### Progress record and stability rule — `stableDeltaMeasurementsStopper` (L1228–1320; instantiated L157–163)
Config: `minDuration: 7`, `maxDuration: 30`, `stabilityDelta: 2` (percent), `minStableMeasurements: 6`
(`measureLatency: false` is passed too but ignored). Per tick (L1808–1817), only when `activeSnapshot`:
```
testComplete = testTime > config.duration.min (5)  &&  stopper({testTime, snapshots, progressMeasurements, speed: currentSpeed})
```
then (L1818–1820) `if (testTime > config.duration.max (30)) { testComplete = true; currentSpeed = aggregator(snapshots) }`.
If not complete and `activeSnapshot`: `progressMeasurements.push({speed: currentSpeed})` (L1841) — i.e. the stopper
sees the **previous** ticks' aggregate speeds as `measurements` and this tick's as `speed`.

`isCompleted` (L1263–1300), with `M = progressMeasurements`, `n = len(M)`, `cur = speed`:
```
if (testTime >= 30) return true
if (n < 6)          return false
maxInd = lastWindowMaxInd(M, ceil(6/2) = 3)            // index of max speed among M[n-3..n-1]; ties → smaller index (>= comparison scanning backwards)
if (n − maxInd < 3) return false                      // ⇔ require M[n-3].speed >= max(M[n-2].speed, M[n-1].speed): speed not rising over last 3 ticks
maxInd = max(0, n − 6); if (n − maxInd < 6) return false   // always passes when n >= 6
if (maxDelta(cur, M[n-6..n-1]) > 2) return false      // maxDelta = max_i 100·|M[i].speed − cur| / cur   (percent of CURRENT aggregate)
return testTime >= 7
```
Plain English: the test ends at the first 150 ms tick, ≥ 7 s after the first target URL arrived, at which the last
six recorded aggregate speeds are all within ±2 % of the current aggregate speed and the aggregate has not risen over
the last three ticks; otherwise it ends at 30 s. (Module 11's `maxDelta` returns the *last* delta instead of the max
— a bug — but module 11 is not used by fast.com.) `collectAfterComplete` is false so the `afterCompleteDuration`
branch is dead.

### Final reported number
`resultData.speed = currentSpeed` = the **aggregator output at the completing tick**, in bits/s (L1821–1827) — a
cumulative-from-peak-window average, **not** a percentile and not the last window. Emitted in `SUCCESS`/`END` as
`speed`, along with `bytes` (phase total) and `latency` (loaded latency object, see e). `stable` is always `true`
(L1832–1834; the `stopperStable=false` path never flips `stable`), so the UI never shows "unstable results".

### Display conversion (`convertSpeed`, L2118–2134)
```
mbps = bits_per_s / 1e3 / 1e3                       // decimal megabits
if (mbps < 1)        { value = mbps * 1e3; units = "Kbps" }
else if (mbps >= 995){ value = mbps / 1e3; units = "Gbps" }
else                 { value = mbps;       units = "Mbps" }
value = value < 9.95 ? round1(value).toFixed(1)     // one decimal
      : value < 100  ? round(value)                 // integer
      :                10 * round(value / 10)       // nearest 10
```
Bytes display (`convertToMB`, L2135–2143): `bytes/1e6`, `>1 && <10` → 1 decimal, `>=10` → nearest 10, `<=1` → 2 decimals.

## e. Latency

### Timing primitives
- Clock: `performance.now()` bound to `window.performance` (`webkitNow` fallback, then `Date.getTime()`), L2095–2112.
- Ping request (`sendLatencyRequest`, L1526–1530): `pinger.upload(template.replace("/range/", "/range/0-0"), 0, …)` →
  **`POST https://<oca>/speedtest/range/0-0?c=..&…` with `send(null)`**, no `Content-Type` (only set when size > 0,
  L946–950). Live response: `200`, `Content-Length: 0`, `Timing-Allow-Origin: *`.
- Sample computation (`onLatencyComplete`, L1693–1741):
  ```
  latency = data.end − data.start
  if (!data.timing) latency /= 2
  ```
  where `extractTimingInfo` (L727–745) looks up `performance.getEntriesByName(rangeUrl)`, takes the **last** entry,
  and if `responseStart || responseEnd` is truthy sets `data.timing = entry`,
  `data.start = requestStart || connectEnd || startTime || fetchTime || data.start`, `data.end = responseStart || responseEnd`.
  Because OCAs send `Timing-Allow-Origin: *`, in practice **latency = `responseStart − requestStart`** of the
  Resource Timing entry — time from the first request byte written to the first response byte received (TTFB on a
  keep-alive connection). Fallback (no entry, e.g. Resource Timing buffer full — default 250 entries, cleared only in
  `reset()` L1455–1457): half the wall-clock round trip measured with `performance.now()` around the XHR.
  Go equivalent: `httptrace` `WroteRequest → GotFirstResponseByte` on a reused connection; do not halve.

### Unloaded latency — dedicated phase (`tester.latency()`, L2031–2042)
- Starts `min(5, connections.max) = 5` workers (L1917–1918), each on the next target in rotation, **no data
  transfer**: each worker just loops `sendLatencyRequest` with `scheduleDelay = 0` (L1723–1731) — back-to-back pings,
  one outstanding per worker.
- Every 150 ms (L1780–1795): `computeUnloadedLatency()` (L1475–1485): for each worker `p = percentile(samples, 0.10)`;
  `value = min over workers of p`; `count = total samples`. Complete when **`testTime > 5 s && count > 50`** (L1784).
  Result `value` (ms) is the reported unloaded latency (event `value`; UI element `latency-value`).
- **There is no maximum duration for this phase** in the JS; if pings keep failing it never ends (ambiguity, see §4).
- `percentile(values, p)` (L3365–3385): drop non-numbers; `p <= 0 → values[0]` (before sorting!); `p >= 1 → last`;
  sort ascending; `idx = (n−1)·p; lo = floor(idx); hi = lo+1; w = idx − lo; return v[lo]·(1−w) + v[hi]·w`.
  **Quirk:** for `n == 1` this evaluates `v[1]·0 = NaN` → returns `NaN`; for `n == 0` returns `undefined`. `Math.min`
  over a list containing `NaN`/`undefined` is `NaN`; the UI renders that as `0`. A Go port should return the single
  value for n == 1 and skip empty workers.

### Loaded latency — during download and upload
- Every worker also runs its pinger from `startTestWorker` (L1749–1751; `measureLatency: true`, L179): after each
  ping completes, the next is scheduled after `max(latencyMeasurementsFrequencyMs (1000) − latency, 0)` ms on
  success, or `1000` ms on failure (L1723–1731) → ≈ 1 ping/s per active connection, up to 8/s total.
- `computeLoadedLatency()` (L1459–1474): for each worker with **more than 4** samples, `percentile(samples, 0.75)`;
  if no worker has > 4 samples, use the p75 of every worker; `value = min over workers`; `count = total samples`.
  Emitted in every `PROGRESS` event (`latency`) and in the final `SUCCESS` payload (`eventData.latency`, L1508).
- UI: the download phase's loaded latency is shown as "Loaded" (`bufferbloat-value`, L2761–2765 and L2969–2976). Upload's
  loaded latency is measured regardless but displayed only if the advanced setting `measureUploadLatency` (default
  false, L2165–2168) is on.
- `latencyMeasurementsWindowSize (5)` and `minLatencyMeasurements (3)` exist in the config (L389–390) but are **never
  read by the engine** (grep: only validator/default occurrences). The `> 4` threshold is hard-coded.
- Display (`convertLatency`, L2144–2161): `< 1000` → `round(ms)` + "ms"; `>= 1000` → one decimal seconds; invalid or
  `> 1e6` → `0`.

## f. Upload phase (`tester.upload()`, L2019–2030; `sendUploadRequest`, L1566–1602; `uploadTest`, L861–960)
- Same range-template rewriting and same attempt/worker-count conditions as download: **`POST
  https://<oca>/speedtest/range/0-<requestSize>?c=..&…`**, unranged `POST …/speedtest?…` only on attempt ≥ 3 with ≤ 2 workers.
- Same size sequence: first request **2048 bytes** (same `supportsProgress` quirk), then `26214400 − ocaRequest` bytes
  (`ocaCount` is reset per attempt/phase, so it restarts at 26214400). Server replies `200` with empty body.
- Body: `utils.genBlob(size)` (L3442–3465): a JS string grown by `s += s + Math.random().toString()` until
  `2·len > size`, then cut/padded to exactly `size` chars → `new Blob([s], {type: "application/octet-stream"})`.
  Content is ASCII digits and `.` (single-byte, so blob byte length == `size`); pseudo-random, moderately compressible.
  Header set explicitly: `Content-type: application/octet-stream` (only when size > 0). `Content-Length` = size.
- Bytes counted via `xhr.upload.onprogress` (L908–920, **no status check**): `e.loaded − lastLoaded` per event; on
  `readyState 4` with `2xx || 304` (L931–937) a final measurement of `size − lastLoaded`; otherwise failure.
  Note this is bytes handed to the browser's network stack, not server-acknowledged bytes.
- Timeouts identical (9 s watchdog to first upload progress, `xhr.timeout = 60000`).
- Concurrency ramp (1 → 3 → 5/8), 150 ms ticks, snapshot merge, `stableMovingAverage(5)`, stopper (7 s / 30 s / 2 % / 6),
  result = aggregator speed in bits/s, unit conversion — all identical to download (same code path).

## g. Phase order and inter-phase behaviour
1. Page load → `tester.download()` immediately (L220–222). Loaded-latency pings run concurrently on every download worker.
2. `ui.onComplete` for a download with `result === "success"` (L2844, L2977–2978): `tester.latency()` (unloaded phase).
   Not gated on "Show more info" — **always runs**; the button only reveals already-measured values (L2568–2596).
3. Latency `END` (any result but "stop") → UI publishes "bufferbloat" = download's `latency.value`, then `tester.upload()`
   (L2960–2978). Loaded-latency pings run concurrently on every upload worker.
4. Upload `END` → done. If download failed/was stopped, neither latency nor upload runs. Re-test (click) → `tester.stop()`
   then `tester.download()` again (`urlGetter.reset()` → fresh API call).
- Nothing is explicitly discarded as warm-up; the 2048-byte first request and the aggregator's ramp-up handling
  play that role. Between phases `reset()` clears all measurements; the aggregator self-resets (n < 5); the target
  list and rotation index carry over (no new API call); `stats` byte counters are per phase.
- `reportEndEvents` fires 10 ms after `stop()` (L1787–1790, L1836–1839).
- Advanced-settings UI (`TEST_CONFIG`, L2163–2197): defaults min/max connections 1/8, min/max duration 5/30,
  `showAdvanced` false, `measureUploadLatency` false; changing duration only alters `config.duration`, **not** the
  stopper's fixed 7/30, so `duration.min` below 7 has no effect and `duration.max` above 30 is capped by the stopper.

## h. Headers, connection reuse, timeouts
- **No `User-Agent`, `Accept`, `Range`, or other explicit request headers** on API, download, or ping requests.
  Only: `Content-type: application/octet-stream` on non-empty uploads (L946–950) and `Content-type: application/json`
  on telemetry POSTs to `https://ichnaea-web.netflix.com/cl2` (L557, L170; telemetry is out of scope — do not replicate).
- No credentials (`withCredentials` unset); CORS relies on `Access-Control-Allow-Origin: *` (present).
- Connection reuse: browser-managed keep-alive pool (OCA answers `Connection: keep-alive`; curl negotiated HTTP/1.1).
  Each worker holds 2 logical request streams (data + ping) to one host; ping TTFB is therefore measured on a warm
  connection. A Go port should reuse one `http.Transport`, keep-alives on, and measure TTFB via `httptrace`.
- Timeouts: per request 9,000 ms until first progress (`requestTimeoutMs`, L971) and `xhr.timeout = 60,000` ms overall
  (L856, L954); API request uses the same requester; telemetry 5,000 ms. Backoff between attempts `min(2^attempt, 200)` ms.

## i. All numeric constants (verbatim)

### Effective fast.com configuration (module 3 overrides applied to module 4 defaults)
| key | value | source |
|---|---|---|
| `version` | `"0.3.12"` | L373 |
| `maxAttempts` | `10` | L177 (default 5, L374) |
| `connections.min` / `.max` | `1` / `8` | L176 (default 1/3, L375) |
| `maxConnections` (unused by engine) | `8` | L376 |
| `duration.min` / `.max` (seconds) | `5` / `30` | L175, L371 |
| `progressFrequencyMs` | `150` | L187 (default 200, L378) |
| `protocol` | `"https"` | L379 |
| `firstRequestBytes` | `2048` | L380 |
| `maxBytesInFlight` | `78643200` | L381 |
| `collectAfterComplete` | `false` | L174 |
| `getTestOcasParams` | `{https:true, endpoint:"api.fast.com/netflix/speedtest/v2", token:"YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm", urlCount:5}` | L180–185 |
| `startRangeRequestTimeMs` (legacy path) | `300` | L384 |
| `maxRangeRequestTimeMs` (legacy path) | `1000` | L385 |
| `rangeRequestIncreaseMs` (legacy path) | `200` | L386 |
| `measureLatency` | `true` | L179 |
| `latencyMeasurementsWindowSize` (never read) | `5` | L389 |
| `minLatencyMeasurements` (never read) | `3` | L390 |
| `latencyMeasurementsFrequencyMs` | `1000` | L391 |
| `aggregator` | `stableMovingAverage(windowSize=5, snapshotResetThreshold=5)` | L156, L108 |
| `stopper` | `stableDeltaMeasurementsStopper{minDuration:7, maxDuration:30, stabilityDelta:2, minStableMeasurements:6}` | L157–163 |

### Hard-coded engine literals
| constant | value | where |
|---|---|---|
| `MAX_PAYLOAD_BYTES` | `26214400` | L1407 |
| requester `requestTimeoutMs` | `9000` | L971 |
| `xhr.timeout` | `60000` | L856, L954 |
| accepted download statuses | `200`, `304` | L816, L841 |
| accepted upload statuses | `200–299`, `304` | L933–935 |
| worker scale thresholds (bit/s) | `5e7 → 8`, `1e7 → 5`, `1e6 → 3`, `5e5 (active<2) → 3` | L1854–1861 |
| latency workers | `min(5, connections.max)` | L1917 |
| unloaded-latency completion | `testTime > 5 s && count > 50` | L1784 |
| unloaded percentile | `0.10` | L1481 |
| loaded percentile / min samples | `0.75` / `> 4` | L1465–1466, L1471 |
| retry backoff | `min(2^attempt, 200)` ms | L1934 |
| end-event delay | `10` ms | L1790, L1839 |
| legacy min range | `1024` bytes | L1682 |
| legacy growth cap | `5 × requestSize` | L1671 |
| bad-URL thresholds | `ocaFailures >= 2`, `siteFailures >= 3` | L3157 |
| refetch threshold | `testUrls.length < 3` | L3236 |
| refresh wait poll | `50` ms | L3224 |
| unit thresholds | `< 1 Mbps → Kbps`, `>= 995 Mbps → Gbps`; rounding at `9.95`, `100` | L2123–2131 |
| latency display | `>= 1000 ms → s`, `> 1e6 → 0` | L2152–2159 |
| validator limits | `duration.max <= 300`, `connections.max <= 30` | L336, L365 |
| telemetry | `batchInterval 6e5`, `batchSize 1e5`, flush every `2000` ms | L615–616, L194 |

## j. Pseudo-code of the full test loop (faithful to the JS)

```
CONST TOKEN="YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm"; URLCOUNT=5; MAXPAYLOAD=26214400; FIRST=2048
CONST TICK=150ms; DUR_MIN=5s; DUR_MAX=30s; STOP_MIN=7s; STOP_MAX=30s; DELTA=2%; NSTABLE=6
CONST PING_PERIOD=1000ms; LOADED_P=0.75; UNLOADED_P=0.10

fetchTargets():
    json = GET https://api.fast.com/netflix/speedtest/v2?https=true&token=TOKEN&urlCount=URLCOUNT   (9s to first byte, 60s total)
    templates = [ t.url.replaceFirst("speedtest","speedtest/range/") for t in json.targets if !isBad(t) ] or all if empty
    rotIdx = 0; return templates                       # first caller gets templates[0]; next callers (rotIdx+1) % len

nextTarget():  if len(templates) < 3: fetchTargets() (concurrent callers wait); else rotIdx=(rotIdx+1)%len; return templates[rotIdx]

runPhase(kind):                                        # kind ∈ {download, upload, latency}
    attempt = 1
    loop:
        resetState()                                   # measurements, snapshots, aggregator(n<5 auto), ocaCount, downloadStart=nil
        if attempt > 10: return FAIL
        if attempt > 1: sleep(min(2^attempt, 200) ms)
        startTime = now()
        nWorkers = (kind == latency) ? 5 : 1
        repeat nWorkers: startWorker(nextTarget())     # downloadStart set when the first target URL is obtained
        result = tickLoop(kind)                        # returns SUCCESS payload, or RESTART(error)
        if result is RESTART: markBad(error.url); attempt += 1; continue
        return result

startWorker(tpl):                                      # tpl = https://host/speedtest/range/?c=..&n=..&v=..&e=..&t=..
    w = new worker{ meas=[], lat=[], size=FIRST, oca=host(tpl) }
    if kind != latency: spawn pingLoop(w, tpl, periodic=true)
    if kind == download: spawn dataLoop(w, tpl, GET)
    if kind == upload:   spawn dataLoop(w, tpl, POST)
    if kind == latency:  spawn pingLoop(w, tpl, periodic=false)

dataLoop(w, tpl, method):
    loop while phase active:
        if attempt < 3 or activeWorkers >= 3:  url = tpl.replace("/range/", "/range/0-" + w.size)   # ranged (normal)
        else:                                  url = tpl.replace("/range/", "");  w.size = MAXPAYLOAD  # unranged
        t0 = now(); last = t0; loaded = 0
        req = method == GET ? GET url : POST url body=randomAsciiBlob(w.size) header Content-type: application/octet-stream
        for each progress chunk (status 200/304 for GET; any for upload progress):     # watchdog: 9s until first chunk; 60s total
            n = now(); w.meas.append({bytes: loaded_now - loaded, start: last, end: n}); last = n; loaded = loaded_now
        on finish ok:  w.meas.append({bytes: total - loaded, start: last, end: now()})
        on failure with w.meas empty:  return RESTART(error{url: tpl})          # whole attempt restarts
        k = ocaCount[w.oca]++;  w.size = MAXPAYLOAD - k                          # 26214400, 26214399, ...

pingLoop(w, tpl, periodic):
    loop while phase active:
        url = tpl.replace("/range/", "/range/0-0")
        t = POST url (empty body) ; lat = TTFB(t)  = firstResponseByte - requestWritten  (fallback: wallclock/2)
        if ok: w.lat.append(lat)
        sleep( periodic ? (ok ? max(PING_PERIOD - lat, 0) : PING_PERIOD) : 0 )

tickLoop(kind):
    progress = []                                      # list of aggregate speeds, one per active tick
    every TICK:
        T = (now() - (downloadStart or startTime)) / 1000
        if kind == latency:
            v = min over workers of percentile(w.lat, UNLOADED_P); count = Σ len(w.lat)
            if T > 5 and count > 50: return SUCCESS{value: v, count}
            continue
        snap = snapshot(allWorkers)                    # §d: needs new data from EVERY worker; union-of-busy-time merge
        complete = false
        if snap and snap.end > snap.start:
            speed = aggregate(snapshots)               # stableMovingAverage(5), bits/s
            complete = T > DUR_MIN and stopper(T, progress, speed)
        if T > DUR_MAX: complete = true; speed = aggregate(snapshots)
        if complete:
            return SUCCESS{ speed, bytes: Σ all bytes this phase, latency: loadedLatency() }
        if snap active: progress.append(speed)
        # scale up (never down)
        if activeWorkers < 8:
            d = activeWorkers
            if speed > 5e7: d = 8  elif speed > 1e7 and active < 5: d = 5  elif speed > 1e6 and active < 3: d = 3
            if speed > 5e5 and active < 2: d = 3
            repeat (d - activeWorkers): startWorker(nextTarget())

stopper(T, M, cur):
    if T >= STOP_MAX: return true
    n = len(M); if n < NSTABLE: return false
    if not (M[n-3] >= M[n-2] and M[n-3] >= M[n-1]): return false            # not still rising
    if max_i∈[n-6,n) 100*|M[i]-cur|/cur > DELTA: return false
    return T >= STOP_MIN

loadedLatency():
    ps = [percentile(w.lat, LOADED_P) for w if len(w.lat) > 4] or [percentile(w.lat, LOADED_P) for all w]
    return { value: min(ps), count: Σ len(w.lat) }

main():
    down = runPhase(download)                          # shows speed (convertSpeed) + loaded latency = down.latency.value
    if down != SUCCESS: stop
    unl  = runPhase(latency)                           # unloaded latency = unl.value (ms)
    up   = runPhase(upload)                            # upload speed; its loaded latency measured but hidden by default
```

## §4. Ambiguities and JS quirks (explicit)

1. **First request is 2048 bytes only because of a missing `return`** in `supportsProgress()` (L1003–1010). Observable
   behaviour is deterministic: `GET/POST /range/0-2048` first, then `/range/0-(26214400−k)`. Recommend replicating
   (it acts as a cheap connection warm-up); the alternative reading (intended 25 MiB first request) is not what ships.
2. **Snapshot early-return bug** (L1050–1063): when any worker has no new data, overflow items of workers iterated
   earlier are discarded. Recommend *not* replicating (keep overflow); effect on results is tiny but nonzero.
3. **`percentile` returns `NaN` for one sample and `undefined` for zero** (L3365–3385); `Math.min` then yields `NaN`,
   rendered as `0`. Recommend: 1 sample → that value; skip workers with 0 samples.
4. **No max duration for the unloaded-latency phase** (L1784); only `T > 5 s && count > 50`. Recommend a cap (e.g. 30 s).
5. **Loaded-latency timing source can silently change**: Resource Timing entries stop being recorded once the
   per-document buffer (250 entries in Chrome) fills; later pings fall back to `wallclock / 2`. A native client should
   always use TTFB from its own transport trace and never halve.
6. **`site` bucket is the literal `"oca"` for all hosts** (L3130), so `siteFailures >= 3` effectively blacklists every
   target after three distinct hosts each fail once, forcing an API refetch (whose all-bad fallback keeps them anyway).
7. **Unranged URL** (`/speedtest?…`, full 25 MiB object) only on attempt ≥ 3 with ≤ 2 workers; ranged otherwise. It is
   ambiguous whether this is intentional; a port may always use the ranged form (server behaviour is identical in size).
8. **Upload bytes are browser-buffered bytes**, not server-acknowledged; early upload measurements may overshoot. A Go
   client counting bytes written to the socket reproduces the same bias.
9. **`stable` is always `true`** (L1832–1834); the "unstable results" UI path is unreachable.
10. **`maxConnections`, `latencyMeasurementsWindowSize`, `minLatencyMeasurements`, `rangeRequestIncreaseMs`,
    `startRangeRequestTimeMs`, `maxRangeRequestTimeMs`, and module 1/11** are present but not exercised by fast.com in
    modern browsers. Module 11's `maxDelta` returns the last delta instead of the maximum (bug; unused).
11. **`client.isp` may be absent** from the API response (absent for a GCP source IP in the observed live response).
12. Speed samples are bit/s over the union of connection-busy time, so a stalled connection lengthens nothing but a
    stalled *all* connections lengthens nothing either (gaps excluded) — i.e. fast.com reports goodput during busy time,
    not bytes / wall-clock. Throughput over pure wall-clock would read lower on bursty links.

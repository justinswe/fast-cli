# fast-cli

`fast` is a command-line reimplementation of [fast.com](https://fast.com)'s speed test in Go. It is not a generic
downloader that hits Netflix's servers: it reproduces fast.com's own methodology — the same token discovery, the same
targets API call, the same 2048-byte warm-up followed by 25 MB range requests, the same 1→3→5→8 connection ramp,
the same 150 ms sampling, the same `stableMovingAverage` aggregator and the same stop rule — so its numbers are meant
to be compared with what fast.com shows in a browser.

The speed-test protocol uses Go's standard library; the compiled binaries have no runtime package dependencies.

## One-line installer

The installer selects the GitHub Release binary for Linux or macOS on amd64 or arm64, verifies it against that
release's `SHA256SUMS`, and runs `fast`. By default it installs to `~/.local/bin/fast` without elevated permissions.
`--no-install` runs from a private temporary directory and removes the binary afterward.

```sh
`curl -fsSL https://speedtest.ju2tin.dev | bash`

Only Speedtest; No install
`curl -fsSL https://speedtest.ju2tin.dev | bash -s -- --no-install`
`curl -fsSL https://speedtest.ju2tin.dev | bash -s -- --no-install -- --upload`
```

The `--` after `--no-install` separates installer options from `fast` options. If `~/.local/bin` is not on `PATH`,
the installer prints a hint; it still runs the installed binary by its full path. For a fetch failure to affect the
pipeline's exit status, enable `pipefail` in the invoking shell. To review the script first, download it with
`curl -fsSL -o install.sh https://speedtest.ju2tin.dev` and run `bash install.sh` after inspection.

## Requirements

- Bazel 9.2.0 (or Bazelisk); Bazel downloads Go 1.27.1.
- Outbound HTTPS to `fast.com`, `api.fast.com` and the Netflix Open Connect servers (`*.oca.nflxvideo.net`) the API
  hands out.

## Build

```
bazel build //fast:fast
bazel test //...
bazel run //fast:fast -- --help
```

`fast` and release asset names use Bazel's `module_version()` from `MODULE.bazel`.
The real-network tests are separate manual targets:
`bazel test //internal/fastcom:live_test //internal/engine:live_test`.

## Releases

Bump `version` in `MODULE.bazel` to release. The next push to `main` creates tag `vX.Y.Z` on that commit and a GitHub
Release with four assets: `fast-vX.Y.Z-linux-amd64`, `fast-vX.Y.Z-linux-arm64`, `fast-vX.Y.Z-darwin-amd64`, and
`fast-vX.Y.Z-darwin-arm64`, plus `SHA256SUMS`. Pushes that don't bump the version skip the release. Download the asset
for your OS and CPU, check its SHA-256 digest against `SHA256SUMS`, and mark it executable (`chmod +x fast-vX.Y.Z-*`).

Enable BuildBuddy Workflows for this repository. Presubmit runs on pull requests to `main`. Each `main` push tests,
builds, releases, and then deploys the installer. The release step needs a trusted BuildBuddy secret named
`GITHUB_RELEASE_TOKEN` with GitHub Contents write access to this repository; it is removed before the publisher invokes
Bazel or Git. Publishing creates a draft, uploads the assets, then publishes it after verification. The deploy step
needs a trusted `GCP_DEPLOYER_KEY` secret: a Cloud Run deployer service-account key.

### Installer hosting

The endpoint image is built with Bazel from a digest-pinned distroless static base and runs on Cloud Run as defined in
[`installer/manifest.yaml`](installer/manifest.yaml). The `main` workflow deploys it after the release step.

## Usage

```
usage: fast [-u|--upload] [--json] [--single-line] [--timeout d]
```

| Flag             | Meaning                                                                                                             |
| ---------------- | ------------------------------------------------------------------------------------------------------------------- |
| `-u`, `--upload` | Also measure upload speed and loaded latency (fast.com "show more info"). Without it only download and latency run. |
| `--json`         | Print one JSON object on stdout at the end instead of live output (schema below).                                   |
| `--single-line`  | Print the final summary on a single line and omit the `Client`/`Server(s)` lines. Live output is unaffected.        |
| `--timeout d`    | Abort the whole run after `d` (a Go duration such as `45s` or `2m`). `0` = no cap (default).                        |

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
  "version": "0.1.0",
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

| Key                                   | Type             | Notes                                                                  |
| ------------------------------------- | ---------------- | ---------------------------------------------------------------------- |
| `version`                             | string           | Version from the Bazel module.                                         |
| `timestamp`                           | string           | RFC 3339 time at which the result was written.                         |
| `download_mbps`                       | number           | Download speed, Mbit/s.                                                |
| `upload_mbps`                         | number or `null` | Upload speed, Mbit/s; `null` when `-u` was not given.                  |
| `latency_ms.unloaded`                 | number           | Unloaded latency (idle-line phase), ms.                                |
| `latency_ms.loaded_download`          | number           | Loaded latency measured during the download phase, ms.                 |
| `latency_ms.loaded_upload`            | number or `null` | Loaded latency during upload; `null` without `-u`.                     |
| `client.ip`                           | string           | Public IP as seen by the fast.com API.                                 |
| `client.asn`                          | string           | Autonomous system number (string even if the API sends a number).      |
| `client.isp`                          | string           | ISP name; empty string if the API omits it.                            |
| `client.city`, `client.country`       | string           | Client location.                                                       |
| `servers[]`                           | array            | The targets returned by the API, in API order.                         |
| `servers[].name`, `servers[].url`     | string           | Target URL as returned by the API (the two are identical in practice). |
| `servers[].city`, `servers[].country` | string           | Target location.                                                       |
| `durations.download_seconds`          | number           | Wall-clock length of the download phase.                               |
| `durations.upload_seconds`            | number or `null` | Wall-clock length of the upload phase; `null` without `-u`.            |

## Exit codes

| Code | Meaning                                                                                                   |
| ---- | --------------------------------------------------------------------------------------------------------- |
| 0    | Success.                                                                                                  |
| 1    | The test failed (network/API error, no targets, or `--timeout` elapsed). The reason is printed to stderr. |
| 2    | Usage error (unknown flag or unexpected positional argument).                                             |
| 130  | Interrupted by Ctrl-C or SIGTERM.                                                                         |

## License

MIT; see [LICENSE](LICENSE).

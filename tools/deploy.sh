#!/usr/bin/env bash
# Pushes the installer image, deploys it to Cloud Run, and checks the live version.
# Kept out of buildbuddy.yaml: the BuildBuddy runner expands $VARS in that file,
# and the multi-line key breaks its YAML parse.
set -euo pipefail
set +x

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
printf '%s' "$GCP_DEPLOYER_KEY" > "${dir}/key.json"
unset GCP_DEPLOYER_KEY
auth="$(printf '_json_key_base64:%s' "$(base64 -w0 "${dir}/key.json")" | base64 -w0)"
printf '{"auths":{"us-west1-docker.pkg.dev":{"auth":"%s"}}}' "$auth" > "${dir}/config.json"
export DOCKER_CONFIG="$dir" GOOGLE_APPLICATION_CREDENTIALS="${dir}/key.json"

flags=(--config=rbe --config=ci --remote_download_outputs=all)
bazel run "${flags[@]}" //:installer_image_push
bazel run "${flags[@]}" //:installer_deploy
version="$(cat "$(bazel cquery "${flags[@]}" --output=files //:release_version)")"
curl -fsS --retry 5 --retry-all-errors https://speedtest.ju2tin.dev/ | grep -qx "version='${version}'"

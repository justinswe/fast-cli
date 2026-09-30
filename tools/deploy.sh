#!/usr/bin/env bash
# Pushes the installer image and deploys it; cloudrun-deploy waits until the new revision is Ready.
# Not inline in buildbuddy.yaml: the runner's cleanup expands $GCP_DEPLOYER_KEY there and breaks the YAML.
set -euo pipefail

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

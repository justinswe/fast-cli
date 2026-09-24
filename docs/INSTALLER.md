# Installer image and Cloud Run deployment

The `//:installer_image` target packages a pure Go Linux amd64 server with `rules_img` on the non-root distroless
static base. The server starts through `std/app`; its `--port` flag maps to Cloud Run's `PORT` environment variable.
The server has only two routes: `/` serves the Bash installer and `/healthz` returns `ok`. Its script names the
version from `MODULE.bazel`; that version must have a published GitHub Release containing the four `fast-vX.Y.Z-*`
assets and `SHA256SUMS` before this image is deployed. The image itself runs on Linux amd64. The downloaded CLI
supports Linux and macOS on amd64 and arm64.

## Build and verify locally

```sh
bazel test //installer:installer_test
bazel build //:installer_image
bazel run //:installer_image_load
docker run --rm --read-only --user 65532:65532 --platform linux/amd64 \
  -p 127.0.0.1:8080:8080 fast-installer:dev
```

In another terminal, check `curl -fsS http://127.0.0.1:8080/healthz` and
`curl -fsS http://127.0.0.1:8080/ | head`. Stop the local container after the check. The image has no shell or
package manager, and the server listens on port 8080 without requiring filesystem writes. The canonical
`gcr.io/distroless/static-debian13:nonroot` image is served from Artifact Registry infrastructure even though its
name retains `gcr.io`.

Some Docker Desktop image stores list a loaded `fast-installer:dev` tag but cannot resolve it for `docker run`.
In that case, find its image ID with `docker images --filter=reference='fast-installer:dev'`, confirm its entrypoint
is `/app/installer` with `docker image inspect IMAGE_ID`, and run or retag that ID locally.

## Publish the versioned image

Use a [Cloud Run domain-mapping region](https://docs.cloud.google.com/run/docs/mapping-custom-domains) such as
`us-west1`. The commands below are for the operator to run after the matching GitHub Release is public. The
repository's tag workflow publishes those release assets; building the installer image does not publish them.

```sh
PROJECT_ID=your-project-id
REGION=us-west1
REPOSITORY=fast-cli
SERVICE=fast-installer
bazel build //:release_version
VERSION="$(cat bazel-bin/release_version.txt)"
TAG="v${VERSION}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPOSITORY}/${SERVICE}"

# Create the GAR Docker repository once, if it does not already exist.
gcloud artifacts repositories create "$REPOSITORY" \
  --repository-format=docker --location="$REGION" --project="$PROJECT_ID"
gcloud auth configure-docker "${REGION}-docker.pkg.dev"
bazel run \
  --//:gar_registry="${REGION}-docker.pkg.dev" \
  --//:gar_repository="${PROJECT_ID}/${REPOSITORY}/${SERVICE}" \
  //:installer_image_push
```

Before the push, confirm the release page lists all four binaries and `SHA256SUMS`, and verify that its tag matches
the Bazel module version. Use the pushed image's immutable `sha256:` digest from GAR for deployment. Keep registry
authentication in the operator's credential store, not in source or Bazel flags.

## Deploy and map the domain

Create a dedicated runtime service account with no application roles. Grant the deployer permission to use it;
Cloud Run also needs permission to pull the image if GAR is in a different project. Then deploy the immutable image:

```sh
gcloud iam service-accounts create "$SERVICE" --project="$PROJECT_ID"
RUNTIME_SA="${SERVICE}@${PROJECT_ID}.iam.gserviceaccount.com"
IMAGE_DIGEST='sha256:<digest-from-GAR>'
gcloud run deploy "$SERVICE" \
  --image="${IMAGE}@${IMAGE_DIGEST}" --region="$REGION" --project="$PROJECT_ID" \
  --port=8080 --min-instances=0 --service-account="$RUNTIME_SA" \
  --allow-unauthenticated
```

Confirm the generated `run.app` URL serves `/healthz` and the versioned script. Verify ownership of `ju2tin.dev`,
then create the direct mapping and apply exactly the DNS records returned by Cloud Run:

```sh
gcloud domains verify ju2tin.dev
gcloud beta run domain-mappings create --service="$SERVICE" \
  --domain=speedtest.ju2tin.dev --region="$REGION" --project="$PROJECT_ID"
gcloud beta run domain-mappings describe --domain=speedtest.ju2tin.dev \
  --region="$REGION" --project="$PROJECT_ID"
```

Wait for DNS and the managed HTTPS certificate, then check `https://speedtest.ju2tin.dev/healthz`, inspect the served
script's version, and exercise both installer modes. A release or image update requires a new release, a matching
versioned image, and a Cloud Run deployment of its digest. Direct Cloud Run domain mapping is a Preview feature
with limited regions and is not Google's recommended production routing option; this deployment accepts that
limitation to avoid a load balancer.

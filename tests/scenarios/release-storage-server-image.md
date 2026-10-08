---
id: release-storage-server-image
status: manual
groups: []
requires: []
automation: none
---

# Release matching driver and storage-server images

## Purpose

Verify that a versioned release publishes the separate storage-server image for
both supported architectures alongside the driver image.

## Preconditions

- The contributor has explicitly authorized a release and selected its `v*` tag.
- Docker Hub repositories `michaelcourcy/lvmo-csi` and
  `michaelcourcy/lvmo-csi-storage-server` exist. The release workflow's
  `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets grant push access to both.
- Docker CLI with Buildx is available for read-only manifest inspection.

## Steps

1. Inspect `.github/workflows/release.yaml`: both image builds must run on the
   same tag trigger and before GitHub release creation. The server build must
   select `Dockerfile.storage-server` and its own fixed repository.
2. For the separately authorized release, follow the workflow run for the chosen
   tag. Allow up to 30 minutes; record both image publication steps and the
   GitHub release step. Never create a release solely to run this scenario
   without the contributor's authorization.
3. Set `VERSION` to that published tag. Run
   `docker buildx imagetools inspect michaelcourcy/lvmo-csi:$VERSION` and
   `docker buildx imagetools inspect michaelcourcy/lvmo-csi-storage-server:$VERSION`.
   Record each digest and verify Linux AMD64 and ARM64 manifests in both images.
4. Download `lvmo-csi-${VERSION#v}.tgz` from that GitHub release into a temporary
   directory. Run `helm show chart <package>`: `version` must be the release tag
   without its leading `v`, and `appVersion` must be the complete tag.
5. Run `helm template release-check <package>
   --set create-storage-server.enabled=true
   --set create-storage-server.source-storage-class=external` as one command,
   without image overrides. Verify both repositories use the published tag;
   check the driver controller, node plugin, node-check init container, server
   and both removal hooks.
6. Render again with `--set image.repository=example.test/driver
   --set image.tag=driver-test
   --set create-storage-server.image.repository=example.test/server
   --set create-storage-server.image.tag=server-test`. Verify all corresponding
   containers use the explicit values. Run `bash scripts/test-release-chart.sh`
   for local stable/prerelease/development packaging and rendering checks.

## Expected

- Both repositories contain the selected version for Linux AMD64 and ARM64.
- The server publication step uses `Dockerfile.storage-server`; the driver
  publication step continues to use the default `Dockerfile`.
- GitHub release creation only runs after both publication steps succeed.
- Packaged chart version and appVersion match the release tag as described above.
- Without overrides, rendered resources select matching published image versions.
- Explicit repository/tag overrides work independently for the two images.

## Evidence

- Tag, commit, workflow run URL and publication results.
- Manifest inspection output with digests and platforms for both repositories.
- Packaged chart metadata, rendered image references and local script output.

## Cleanup

Remove temporary downloaded packages and local render output. Keep published release artifacts; do not
delete tags, images or GitHub releases as test cleanup.

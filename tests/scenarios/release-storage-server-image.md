---
id: release-storage-server-image
status: manual
groups: []
requires: []
automation: none
---

# Release matching driver and storage-server images

> Two-chart release revision; local packaging checks do not establish that
> a release has published the images and charts.

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
4. Download both `lvmo-csi-${VERSION#v}.tgz` and
   `lvmo-csi-storage-server-${VERSION#v}.tgz` from that GitHub release into a
   temporary directory. Inspect both with `helm show chart`: chart names must
   differ, versions must equal the tag without `v`, and appVersions the full tag.
   Run `helm repo add lvmo https://michaelcourcy.github.io/lvmo-csi` and
   `helm repo update`; verify `helm search repo lvmo --versions --devel` lists
   both chart names at the selected version. Download each with `helm pull
   lvmo/<chart-name> --version "${VERSION#v}" --destination <temporary-directory>`
   and compare its checksum with the matching GitHub release download.
5. Render the driver package using `helm template release-check <driver-package>
   --api-versions snapshot.storage.k8s.io/v1/VolumeSnapshotClass`.
   Render the server package using `helm template server-check <server-package>
   --set source-storage-class=external`. Without image overrides, the driver
   controller, node plugin and node-check use the driver repository at the tag;
   the server and every server hook use the server repository at the same tag.
   Driver output must contain no storage-server workload; server output must
   contain no CSI driver resources or VolumeSnapshotClass.
6. Keep the advertised snapshot API on driver renders. Render each package again with its own `image.repository` and `image.tag`:
   `example.test/driver:driver-test` for the driver and
   `example.test/server:server-test` for the server. Check every corresponding
   container, including hooks. Run the updated `bash scripts/test-release-chart.sh`
   for local stable/prerelease/development packaging and rendering checks.

## Expected

- Both repositories contain the selected version for Linux AMD64 and ARM64.
- The server publication step uses `Dockerfile.storage-server`; the driver
  publication step continues to use the default `Dockerfile`.
- GitHub release creation only runs after both publication steps succeed.
- Both chart packages are attached to the release and discoverable/downloadable
  from the existing GitHub Pages Helm repository, including prereleases.
- Both packaged chart versions and appVersions match the release tag.
- Rendered resources respect the independent chart ownership boundary.
- Without overrides, rendered resources select matching published image versions.
- Explicit repository/tag overrides work independently for the two images.

## Evidence

- Tag, commit, workflow run URL and publication results.
- Manifest inspection output with digests and platforms for both repositories.
- Both packaged chart metadata, repository index/search output, package
  checksums, rendered image references and local script output.

## Cleanup

Remove temporary downloaded packages and local render output. Keep published release artifacts; do not
delete tags, images or GitHub releases as test cleanup.

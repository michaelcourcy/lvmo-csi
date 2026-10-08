# Keep Kind/node versions pinned together; this customization is tested here,
# not an upstream guarantee about Kind image internals.
FROM kindest/node:v1.35.0
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends open-iscsi nfs-common kmod && rm -rf /var/lib/apt/lists/*

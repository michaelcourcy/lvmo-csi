FROM --platform=$BUILDPLATFORM golang:1.26.3 AS build
ARG TARGETOS=linux TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-X main.version=$VERSION" -o /lvmo-driver ./cmd/lvmo-driver && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /lvmo-metadata ./cmd/lvmo-metadata
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nfs-common open-iscsi util-linux e2fsprogs xfsprogs ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /lvmo-driver /usr/local/bin/lvmo-driver
COPY --from=build /lvmo-metadata /usr/local/bin/lvmo-metadata
ENTRYPOINT ["/usr/local/bin/lvmo-driver"]

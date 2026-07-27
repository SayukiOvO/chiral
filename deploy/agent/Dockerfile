# Build stage
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chiral-agent ./agent/cmd/agent

# Xray-core stage: bundled into the image. Chiral tracks Xray-core's SNAPSHOT
# (prerelease) channel — the latest features (xhttp up/down split, post-quantum
# key exchange, ...) land there, not in the infrequent stable tags. Leave
# XRAY_VERSION empty to fetch the latest snapshot at build time, or pin a
# specific tag (e.g. --build-arg XRAY_VERSION=v26.7.11) for reproducibility.
FROM alpine:3.20 AS xray
ARG XRAY_VERSION=
ARG TARGETARCH
RUN apk add --no-cache curl unzip jq \
    && case "$TARGETARCH" in \
         amd64) XRAY_ARCH=64 ;; \
         arm64) XRAY_ARCH=arm64-v8a ;; \
         *) echo "unsupported arch: $TARGETARCH" && exit 1 ;; \
       esac \
    && VERSION="$XRAY_VERSION" \
    && if [ -z "$VERSION" ]; then \
         VERSION=$(curl -fsSL "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=1" | jq -r '.[0].tag_name'); \
       fi \
    && case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac \
    && echo "Fetching Xray-core $VERSION (snapshot channel)" \
    && curl -fsSL -o /tmp/xray.zip "https://github.com/XTLS/Xray-core/releases/download/${VERSION}/Xray-linux-${XRAY_ARCH}.zip" \
    && unzip /tmp/xray.zip -d /opt/xray

# Runtime stage
FROM alpine:3.20
COPY --from=build /out/chiral-agent /usr/local/bin/chiral-agent
COPY --from=xray /opt/xray/xray /usr/local/bin/xray
COPY --from=xray /opt/xray/geoip.dat /opt/xray/geosite.dat /usr/local/share/xray/
ENV XRAY_LOCATION_ASSET=/usr/local/share/xray \
    CHIRAL_XRAY_BIN=/usr/local/bin/xray \
    CHIRAL_STATE_DIR=/var/lib/chiral-agent
# Holds the node's identity, its applied config, and every kernel version it has
# been upgraded to — including the `active` pointer that keeps a restart from
# dropping back to the binary above. Lose this volume and the node re-registers
# from scratch on the image's Xray.
VOLUME /var/lib/chiral-agent
ENTRYPOINT ["chiral-agent"]

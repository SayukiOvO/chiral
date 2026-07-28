# Build stage. Cross-compiled from the build platform — see the panel image for
# why this is not left to emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/chiral-agent ./agent/cmd/agent

# Xray-core stage: bundled into the image. Chiral tracks Xray-core's SNAPSHOT
# (prerelease) channel — the latest features (xhttp up/down split, post-quantum
# key exchange, ...) land there, not in the infrequent stable tags. Leave
# XRAY_VERSION empty to fetch the latest snapshot at build time, or pin a
# specific tag (e.g. --build-arg XRAY_VERSION=v26.7.11) for reproducibility.
# Build platform: downloads and unpacks for TARGETARCH, never executes it.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS xray
ARG XRAY_VERSION=
ARG TARGETARCH
# GITHUB_TOKEN is optional and mounted as a build secret, never an ARG: an ARG
# is baked into the image history for anyone who pulls it. It only lifts the
# anonymous API rate limit — 60/hour per IP, which CI runners share — so a build
# without it works until that limit is hit.
RUN --mount=type=secret,id=GITHUB_TOKEN \
    apk add --no-cache curl unzip jq \
    && AUTH="" \
    && if [ -s /run/secrets/GITHUB_TOKEN ]; then \
         AUTH="Authorization: Bearer $(cat /run/secrets/GITHUB_TOKEN)"; \
       fi \
    && case "$TARGETARCH" in \
         amd64) XRAY_ARCH=64 ;; \
         arm64) XRAY_ARCH=arm64-v8a ;; \
         *) echo "unsupported arch: $TARGETARCH" && exit 1 ;; \
       esac \
    && VERSION="$XRAY_VERSION" \
    && if [ -z "$VERSION" ]; then \
         VERSION=$(curl -fsSL ${AUTH:+-H "$AUTH"} "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=1" | jq -r '.[0].tag_name'); \
       fi \
    # An unreachable GitHub API leaves VERSION empty, and the pipeline above
    # cannot fail on its own: jq reads the empty stream and exits 0. Without
    # this guard the case-arm below "corrects" the empty string to a bare "v"
    # and the build dies later on a 404 for .../download/v/... — a nonsense URL
    # that says nothing about what actually went wrong. Fail here, with words.
    && { [ -n "$VERSION" ] && [ "$VERSION" != "null" ] \
         || { echo "could not determine the latest Xray version (is api.github.com reachable from the build? pin one with --build-arg XRAY_VERSION=...)" >&2; exit 1; }; } \
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

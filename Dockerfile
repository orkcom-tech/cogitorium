# Stage 1 — build the web UI
FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2 — build the Go binary with the UI embedded
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/cogitorium ./cmd/cogitorium

# Stage 2b — Contextverse, built from its own module rather than vendored.
#
# Requirement 15: this installs together with Contextverse. On the container
# channel that means the image carries contextd, because a compose file cannot
# ask a package manager for it and there is no published contextd image to pull
# (checked, not assumed — Contextverse ships binaries, not containers).
#
# Pinned to a tag rather than @latest: an image that rebuilds into a different
# Contextverse than it was tested against is a supply chain nobody is watching.
FROM golang:1.26-alpine AS contextd
ARG CONTEXTD_VERSION=v0.30.0
RUN CGO_ENABLED=0 go install github.com/orkcom-tech/contextverse/cmd/contextd@${CONTEXTD_VERSION}

# Stage 3 — runtime
FROM alpine:3.21
# /data and the context space are created with the runtime user as owner so a
# fresh named volume inherits writable permissions.
#
# The space lives at contextd's OWN default — ~/.context — rather than
# somewhere tidier. Cogitorium invokes `contextd` without --dir, so the default
# is the only path that is actually consulted; putting the volume anywhere else
# produced a container that reported context as unavailable and, worse, wrote
# it to a layer that a rebuild throws away.
RUN adduser -D cogitorium \
 && mkdir -p /data /home/cogitorium/.context \
 && chown -R cogitorium:cogitorium /data /home/cogitorium
COPY --from=build /out/cogitorium /usr/local/bin/cogitorium
COPY --from=contextd /go/bin/contextd /usr/local/bin/contextd
# The licence and its NOTICE travel with the image for the same reason they
# travel with the archives: pulling it is a redistribution.
COPY LICENSE NOTICE /usr/share/doc/cogitorium/
COPY packaging/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
USER cogitorium
VOLUME /data /home/cogitorium/.context
EXPOSE 8688
# Defaults via env, not baked flags, so `docker run -e COGITORIUM_LISTEN=…`
# and compose environment overrides actually work.
ENV COGITORIUM_LISTEN=0.0.0.0:8688 \
    COGITORIUM_DATA_DIR=/data
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]

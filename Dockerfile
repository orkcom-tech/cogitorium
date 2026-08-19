# Stage 1 — build the web UI
# --platform=$BUILDPLATFORM: this stage runs on the machine doing the building,
# once, whatever architectures are being produced. Its output is a directory of
# static files, which has no architecture at all.
#
# Without it, a multi-arch build runs npm twice, and the second time under QEMU:
# measured at more than eleven minutes for `npm ci` against about one native,
# which is what took a release past its timeout and had it reported as
# "cancelled".
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2 — build the Go binary with the UI embedded
# Cross-compiled rather than emulated. Go does this natively and always has;
# running an arm64 toolchain under QEMU to produce an arm64 binary is paying an
# emulator to do what the compiler already does with a variable.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/cogitorium ./cmd/cogitorium

# Stage 2b — Contextverse, built from its own module rather than vendored.
#
# Requirement 15: this installs together with Contextverse. On the container
# channel that means the image carries contextd, because a compose file cannot
# ask a package manager for it and there is no published contextd image to pull
# (checked, not assumed — Contextverse ships binaries, not containers).
#
# Pinned to a tag rather than @latest: an image that rebuilds into a different
# Contextverse than it was tested against is a supply chain nobody is watching.
#
# AT OR ABOVE update.MinContextd, which is the version this build says it needs
# and warns about at runtime. It was v0.30.0 for a while, which is below both
# that floor and v0.31.0 — where `file delete` and `--if-version` first appeared
# — so the official image shipped a contextd that could not do compare-and-swap
# saves and tripped the product's own compatibility warning on first start.
# Raising this is not optional maintenance: MinContextd is the promise, and a
# pin below it makes the image the one deployment that breaks the promise.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS contextd
ARG CONTEXTD_VERSION=v1.0.0
ARG TARGETOS
ARG TARGETARCH
# The same cross-compile, and this is where the cost actually showed: 800
# seconds under arm64 emulation against 133 native, for one `go install`.
#
# A cross-compiled `go install` writes to $GOPATH/bin/$GOOS_$GOARCH/ rather
# than $GOPATH/bin/, so the binary moves depending on whether the build host
# happens to match the target. GOBIN would pin it — and Go refuses GOBIN with a
# cross-compile outright ("cannot install cross-compiled binaries when GOBIN is
# set"), which is how that attempt failed rather than silently producing the
# wrong thing. So it is found and put somewhere fixed.
#
# THE VERSION IS STAMPED IN, and that is not cosmetic. contextd takes its
# version from an ldflag its own release sets; a plain `go install` leaves the
# default, so the binary reports "0.0.0-dev" whatever tag it was built from.
# Cogitorium reads that string to decide whether the contextd it found is old
# enough to warn about, and treats an unparseable one as a development build
# somebody made deliberately — correctly, because it cannot know.
#
# So an unstamped install made this image INVISIBLE to its own compatibility
# check: it carried v0.30.0, which is genuinely too old, and reported a version
# that meant "do not judge me". Stamped, the container tells the truth about
# itself and the check does its job here like anywhere else.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go install \
            -ldflags "-X github.com/orkcom-tech/contextverse/internal/version.Version=${CONTEXTD_VERSION#v}" \
            github.com/orkcom-tech/contextverse/cmd/contextd@${CONTEXTD_VERSION} \
    && mkdir -p /out \
    && cp "$(find /go/bin -type f -name contextd | head -1)" /out/contextd

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
# libstdc++ is what makes JavaScript-on-a-fetched-runtime work here at all:
# the musl Node and Bun builds will not start without libgcc_s.so.1 and die
# with a shared-library error that says nothing about the cause. 942 KB down,
# under 3 MB installed, and a build-time decision because a container with a
# read-only rootfs can never make it later.
RUN apk add --no-cache libstdc++
RUN adduser -D cogitorium \
 && mkdir -p /data /home/cogitorium/.context /usr/share/cogitorium/ref \
 && chown -R cogitorium:cogitorium /data /home/cogitorium \
 && chmod -R a+rX /usr/share/cogitorium/ref
COPY --from=build /out/cogitorium /usr/local/bin/cogitorium
COPY --from=contextd /out/contextd /usr/local/bin/contextd
# The licence and its NOTICE travel with the image for the same reason they
# travel with the archives: pulling it is a redistribution.
COPY LICENSE NOTICE /usr/share/doc/cogitorium/
# The ref tree is readable by MODE, never by owner, and that is the detail the
# cluster channel turns on. There is no single runtime user: adduser above gives
# uid 1000 and the Helm pod overrides to 65532, which has no passwd entry here.
# An ownership-based ref tree reads fine under compose and is INVISIBLE in the
# cluster — silently emptying the plugin set on exactly the channel where the
# seed is the whole guarantee. The PVC only works because fsGroup chowns it, and
# an image layer gets no fsGroup treatment.
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

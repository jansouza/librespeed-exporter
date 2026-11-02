# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26

# Build the exporter for the target platform on the build platform.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -o /out/librespeed-exporter \
      -ldflags "-s -w \
        -X github.com/prometheus/common/version.Version=${VERSION} \
        -X github.com/prometheus/common/version.Revision=${REVISION} \
        -X github.com/prometheus/common/version.BuildUser=docker \
        -X github.com/prometheus/common/version.BuildDate=$(date -u +%Y%m%d-%H:%M:%S)" \
      ./cmd/librespeed-exporter

# Download the librespeed-cli release for the target platform and verify its
# checksum. Release assets name 32-bit ARM as armv6/armv7.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS cli
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
# Keep in sync with LIBRESPEED_CLI_VERSION in the Makefile.
ARG LIBRESPEED_CLI_VERSION=1.0.14
COPY scripts/fetch-librespeed-cli.sh /usr/local/bin/
RUN arch=$TARGETARCH; \
    if [ "$TARGETARCH" = arm ]; then arch="arm${TARGETVARIANT:-v7}"; fi; \
    fetch-librespeed-cli.sh "$LIBRESPEED_CLI_VERSION" "$TARGETOS" "$arch" /out

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/librespeed-exporter /usr/local/bin/librespeed-exporter
COPY --from=cli /out/librespeed-cli /usr/local/bin/librespeed-cli
COPY --from=cli /out/LICENSE /usr/share/licenses/librespeed-cli/LICENSE
COPY LICENSE /usr/share/licenses/librespeed-exporter/LICENSE
EXPOSE 9469
ENTRYPOINT ["/usr/local/bin/librespeed-exporter"]
CMD ["--config.file=/etc/librespeed-exporter/config.yml"]

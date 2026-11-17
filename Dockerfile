# Argus hub image: a static binary on scratch, plus agent binaries for every
# platform so `curl … | sh` installs work straight from the hub.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go *.s ./
COPY web ./web
ARG VERSION=0.3.0
RUN set -e; mkdir -p /out/dist; \
    for t in linux/amd64 linux/arm64 linux/riscv64 linux/arm darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 freebsd/amd64; do \
      os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/dist/argus-$os-$arch$ext . ; \
    done; \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/argus .

FROM scratch
COPY --from=build /out/argus /argus
COPY --from=build /out/dist /dist
VOLUME /data
EXPOSE 7070
ENTRYPOINT ["/argus"]
CMD ["hub", "--data", "/data", "--dist", "/dist", "--no-local"]

# review-mcp image for serve mode. Not published in v1: this file exists so
# you can build your own image and so CI proves that it builds.
#
#   docker build -t review-mcp:local \
#     --build-arg VERSION=1.0.0-rc.1 --build-arg COMMIT=$(git rev-parse HEAD) .
#
# Both base images are pinned by digest (the multi-arch index digest).
# Refresh them deliberately; the golang tag must match the go directive in
# go.mod.

# golang:1.26.0
FROM golang:1.26.0@sha256:fb612b7831d53a89cbc0aaa7855b69ad7b0caf603715860cf538df854d047b84 AS build

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# The same flags as the release build (.goreleaser.yaml).
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -buildid= \
      -X github.com/nevzatcirak/review-mcp/internal/version.Version=${VERSION} \
      -X github.com/nevzatcirak/review-mcp/internal/version.Commit=${COMMIT}" \
    -o /out/review-mcp ./cmd/review-mcp

# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/review-mcp /review-mcp

EXPOSE 8787
ENTRYPOINT ["/review-mcp"]
CMD ["serve", "--listen", "0.0.0.0:8787"]

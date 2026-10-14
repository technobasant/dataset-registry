# syntax=docker/dockerfile:1
FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/registry ./cmd/registry && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/drbench ./cmd/drbench

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/registry /out/drbench /usr/local/bin/
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/registry"]
CMD ["serve"]

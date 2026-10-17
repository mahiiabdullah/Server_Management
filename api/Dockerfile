# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && mkdir -p /out/targets

# Distroless: no shell, no package manager. Health checks use `api healthcheck` (decision D4).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
# /targets is owned by nonroot so a fresh named volume mounted there is writable.
COPY --from=build --chown=nonroot:nonroot /out/targets /targets
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]

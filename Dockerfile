# syntax=docker/dockerfile:1

# ---- builder ----
FROM golang:1.25-bookworm AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/adduser ./cmd/adduser
RUN mkdir -p /out/data

# ---- final ----
FROM gcr.io/distroless/static-debian12 AS final
WORKDIR /
COPY --from=builder /out/server /server
COPY --from=builder /out/adduser /adduser
COPY --from=builder /out/data /data

VOLUME ["/data"]
EXPOSE 2222 2223

# Public listener has to be reachable from outside the container by default;
# ADMIN_HOST is deliberately left unset here — see docs/admin-guide.md's
# Docker/Unraid section for why there's no safe container-level default.
ENV SSH_HOST=0.0.0.0

ENTRYPOINT ["/server"]

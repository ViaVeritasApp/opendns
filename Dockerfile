FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/opendns ./cmd/opendns

# Pin to the nonroot distroless variant (runs as uid:gid 65532). For fully
# reproducible builds, append a digest, e.g.
#   gcr.io/distroless/static:nonroot@sha256:<digest>
FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/opendns /opendns

# Default ports: 53/udp+tcp for DNS, 8080/tcp for admin.
# Runs as the non-root 65532 user. Binding :53 (a privileged port) as non-root
# requires the NET_BIND_SERVICE capability at runtime — the shipped
# docker-compose.yml grants exactly that and drops all others. If you can't
# grant it, set OPENDNS_DNS_BIND to a high port (e.g. :5353) and front it with
# NAT.
USER 65532:65532
EXPOSE 53/udp 53/tcp 8080/tcp

ENTRYPOINT ["/opendns"]

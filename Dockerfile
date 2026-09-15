# Version pinned to match go.mod's `go 1.26.0` exactly.
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/croupier ./cmd/croupier

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/croupier /usr/local/bin/croupier
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/croupier"]

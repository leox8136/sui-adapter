FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sui-adapter .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S adapter \
    && adduser -S -G adapter adapter

COPY --from=builder /sui-adapter /usr/local/bin/sui-adapter

USER adapter
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sui-adapter"]

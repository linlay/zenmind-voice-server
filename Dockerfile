FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/voice-server ./cmd/voice-server

FROM alpine:3.21

WORKDIR /app

RUN adduser -D -H -u 10001 appuser

COPY --from=builder /out/voice-server /app/voice-server

EXPOSE 11953

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:11953/actuator/health >/dev/null || exit 1

USER appuser

ENTRYPOINT ["/app/voice-server"]

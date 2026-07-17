FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY conformance ./conformance
COPY internal ./internal
COPY pkg ./pkg
RUN ! command -v python && ! command -v python3
RUN go run ./cmd/paritycheck
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ytdlp-go ./cmd/ytdlp-go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ytdlp-js-helper ./cmd/ytdlp-js-helper
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jscheck ./cmd/jscheck

FROM scratch
COPY --from=build /out/ytdlp-go /ytdlp-go
COPY --from=build /out/ytdlp-js-helper /ytdlp-js-helper
COPY --from=build /out/jscheck /jscheck
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENTRYPOINT ["/ytdlp-go"]

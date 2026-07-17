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

FROM scratch
COPY --from=build /out/ytdlp-go /ytdlp-go
ENTRYPOINT ["/ytdlp-go"]

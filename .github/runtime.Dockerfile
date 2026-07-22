FROM golang:1.25.12-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY conformance ./conformance
COPY internal ./internal
COPY pkg ./pkg
RUN ! command -v python && ! command -v python3
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ytdlp-go ./cmd/ytdlp-go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ytdlp-js-helper ./cmd/ytdlp-js-helper

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

RUN apk add --no-cache ca-certificates ffmpeg \
    && ! command -v python \
    && ! command -v python3 \
    && addgroup -S -g 65532 ytdlp \
    && adduser -S -D -H -u 65532 -G ytdlp ytdlp \
    && mkdir /downloads \
    && chown 65532:65532 /downloads
COPY --from=build /out/ytdlp-go /usr/local/bin/ytdlp-go
COPY --from=build /out/ytdlp-js-helper /usr/local/bin/ytdlp-js-helper
COPY LICENSE NOTICE /licenses/
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY third_party/licenses /licenses/third_party

USER 65532:65532
WORKDIR /downloads
VOLUME ["/downloads"]
ENTRYPOINT ["/usr/local/bin/ytdlp-go"]

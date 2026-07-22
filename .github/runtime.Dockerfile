FROM golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build

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

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d AS media-build

ARG FFMPEG_VERSION=6.1.2
ARG FFMPEG_SHA256=3b624649725ecdc565c903ca6643d41f33bd49239922e45c9b1442c63dca4e38
ARG LAME_VERSION=3.100
ARG LAME_SHA256=ddfe36cab873794038ae2c1210557ad34857a4b6bdc515785d1da9e175b1da1e
RUN apk add --no-cache \
        build-base=0.5-r3 \
        ca-certificates=20260413-r0 \
        nasm=2.16.03-r0 \
        pkgconf=2.3.0-r0 \
        xz=5.8.3-r0 \
    && ! command -v python \
    && ! command -v python3
WORKDIR /build
ADD --checksum=sha256:3b624649725ecdc565c903ca6643d41f33bd49239922e45c9b1442c63dca4e38 https://sources.buildroot.net/ffmpeg/ffmpeg-6.1.2.tar.xz /build/ffmpeg-6.1.2.tar.xz
ADD --checksum=sha256:ddfe36cab873794038ae2c1210557ad34857a4b6bdc515785d1da9e175b1da1e https://sources.buildroot.net/lame/lame-3.100.tar.gz /build/lame-3.100.tar.gz
RUN echo "${FFMPEG_SHA256}  ffmpeg-${FFMPEG_VERSION}.tar.xz" | sha256sum -c - \
    && echo "${LAME_SHA256}  lame-${LAME_VERSION}.tar.gz" | sha256sum -c - \
    && tar -xf "ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    && tar -xf "lame-${LAME_VERSION}.tar.gz"
WORKDIR /build/lame-3.100
RUN ./configure \
        --prefix=/opt/lame \
        --disable-frontend \
        --disable-static \
        --enable-shared \
    && make -j2 \
    && make install
WORKDIR /build/ffmpeg-6.1.2
RUN export PKG_CONFIG_PATH=/opt/lame/lib/pkgconfig \
        LD_LIBRARY_PATH=/opt/ffmpeg/lib:/opt/lame/lib; \
    ./configure \
        --prefix=/opt/ffmpeg \
        --disable-autodetect \
        --disable-debug \
        --disable-doc \
        --disable-ffplay \
        --disable-gpl \
        --disable-nonfree \
        --disable-static \
        --enable-libmp3lame \
        --enable-shared \
        --extra-cflags=-I/opt/lame/include \
        --extra-ldflags=-L/opt/lame/lib \
    && make -j2 \
    && make install \
    && ./ffmpeg -version | grep -F -- '--disable-gpl' \
    && ./ffmpeg -version | grep -F -- '--disable-nonfree'

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

RUN apk add --no-cache ca-certificates=20260413-r0 \
    && ! command -v python \
    && ! command -v python3 \
    && addgroup -S -g 65532 ytdlp \
    && adduser -S -D -H -u 65532 -G ytdlp ytdlp \
    && mkdir /downloads \
    && mkdir -p /usr/share/source /licenses/runtime \
    && chmod 0755 /usr/share/source /licenses/runtime \
    && chown 65532:65532 /downloads
COPY --from=build /out/ytdlp-go /usr/local/bin/ytdlp-go
COPY --from=build /out/ytdlp-js-helper /usr/local/bin/ytdlp-js-helper
COPY --from=media-build /opt/ffmpeg/bin/ /usr/local/bin/
COPY --from=media-build /opt/ffmpeg/lib/ /usr/local/lib/
COPY --from=media-build /opt/lame/lib/ /usr/local/lib/
COPY --chmod=0644 --from=media-build /build/ffmpeg-6.1.2.tar.xz /usr/share/source/ffmpeg-6.1.2.tar.xz
COPY --chmod=0644 --from=media-build /build/lame-3.100.tar.gz /usr/share/source/lame-3.100.tar.gz
COPY --from=media-build /build/ffmpeg-6.1.2/COPYING.LGPLv2.1 /licenses/runtime/COPYING.LGPLv2.1
COPY --from=media-build /build/ffmpeg-6.1.2/LICENSE.md /licenses/runtime/FFMPEG-LICENSE.md
COPY --from=media-build /build/lame-3.100/COPYING /licenses/runtime/LAME-COPYING
COPY LICENSE NOTICE /licenses/
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY third_party/licenses /licenses/third_party

USER 65532:65532
WORKDIR /downloads
VOLUME ["/downloads"]
ENTRYPOINT ["/usr/local/bin/ytdlp-go"]

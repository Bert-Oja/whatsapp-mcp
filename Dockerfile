# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1: minimal ffmpeg
#
# Only send_audio_message needs ffmpeg: converting an attached audio file to
# Opus in Ogg for a WhatsApp voice note. The distribution builds are enormous
# (apk add ffmpeg adds ~188MB across 123 packages; a prebuilt static ffmpeg is
# a 144MB binary), so this compiles a stripped encoder-only ffmpeg instead.
#
# Input formats cover what users realistically attach; the output is always
# Opus in Ogg.
FROM alpine:3.21 AS ffmpeg-build
RUN apk add --no-cache build-base pkgconf curl xz opus-dev binutils nasm
RUN curl -fsSL https://ffmpeg.org/releases/ffmpeg-7.1.tar.xz -o /tmp/ffmpeg.tar.xz \
 && tar -C / -xf /tmp/ffmpeg.tar.xz
WORKDIR /ffmpeg-7.1
RUN ./configure \
      --disable-everything \
      --disable-doc \
      --disable-network \
      --disable-avdevice \
      --disable-swscale \
      --disable-postproc \
      --disable-ffplay \
      --disable-ffprobe \
      --enable-ffmpeg \
      --enable-libopus \
      --enable-encoder=libopus \
      --enable-decoder=pcm_s16le \
      --enable-decoder=pcm_s24le \
      --enable-decoder=pcm_f32le \
      --enable-decoder=mp3 \
      --enable-decoder=aac \
      --enable-decoder=flac \
      --enable-decoder=vorbis \
      --enable-decoder=opus \
      --enable-demuxer=wav \
      --enable-demuxer=mp3 \
      --enable-demuxer=mov \
      --enable-demuxer=flac \
      --enable-demuxer=ogg \
      --enable-demuxer=matroska \
      --enable-parser=opus \
      --enable-parser=mpegaudio \
      --enable-parser=aac \
      --enable-muxer=ogg \
      --enable-protocol=file \
      --enable-filter=aresample \
      --pkg-config-flags=--static \
      --disable-shared --enable-static \
 && make -j"$(nproc)" ffmpeg \
 && strip ffmpeg

# ---------------------------------------------------------------------------
# Stage 2: build the Go binary
#
# CGO is disabled: the pure-Go SQLite driver (modernc.org/sqlite) needs no C
# toolchain, which is what lets the runtime stage stay minimal.
FROM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/whatsapp-mcp ./cmd/whatsapp-mcp

# ---------------------------------------------------------------------------
# Stage 3: runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates opus tzdata \
 && adduser -D -H -u 10001 app
COPY --from=go-build /out/whatsapp-mcp /usr/local/bin/whatsapp-mcp
COPY --from=ffmpeg-build /ffmpeg-7.1/ffmpeg /usr/local/bin/ffmpeg

# Session and message data live here; mount a volume to persist them.
RUN mkdir -p /data && chown app:app /data
VOLUME ["/data"]

USER app
ENV STORE_DIR=/data \
    MCP_HTTP_ADDR=127.0.0.1:8765 \
    ALLOW_WRITES=false

# The MCP endpoint and health probe are bound to loopback inside the
# container, so the port must be published to the host explicitly.
EXPOSE 8765

# /healthz reports 503 until WhatsApp is connected, so the container is only
# healthy once the session is actually usable.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8765/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/whatsapp-mcp"]

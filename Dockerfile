# SPMAG Wifi Photo Receiver — headless receiver (wpr watch) for Linux servers/NAS.
#
#   docker build -t spmag-wpr .
#   docker run -d --name spmag-wpr --network host --restart unless-stopped \
#     -v /srv/photos/camera:/photos -v spmag-wpr-config:/config spmag-wpr
#
# --network host is required: the camera is found by SSDP multicast.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/wpr ./cmd/wpr

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/wpr /usr/local/bin/wpr
ENV XDG_CONFIG_HOME=/config HOME=/config
VOLUME ["/photos", "/config"]
ENTRYPOINT ["/usr/local/bin/wpr", "watch", "-dest", "/photos"]

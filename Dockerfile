# SPMAG Wifi Photo Receiver — headless receiver (wpr watch) for Linux servers/NAS.
#
#   docker build -t spmag-wpr .
#   docker run -d --name spmag-wpr --network host --restart unless-stopped \
#     -e SPMAG_WEB=:8080 -e SPMAG_WEB_PASSWORD='choose-a-long-password' \
#     -v /srv/photos/camera:/photos -v spmag-wpr-config:/config spmag-wpr
#
# --network host is required: the camera is found by SSDP multicast.
# SPMAG_WEB turns on the browser interface (http://HOST:8080); on a network
# address it refuses to start without SPMAG_WEB_PASSWORD. Leave both unset for a
# headless receiver. USB pairing from the web page needs the camera's SCSI
# device: add  --device /dev/sg1  (see "wpr usb" on the host).
FROM golang:1.27-alpine AS build
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

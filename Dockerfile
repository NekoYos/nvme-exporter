FROM golang:1.27.1-alpine3.24 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY testdata ./testdata
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nvme-exporter .

FROM alpine:3.24
RUN apk add --no-cache nvme-cli
COPY --from=build /out/nvme-exporter /usr/local/bin/nvme-exporter
EXPOSE 9998
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O /dev/null http://127.0.0.1:9998/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/nvme-exporter"]

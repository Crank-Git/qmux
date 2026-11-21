# The build stage makes a static qmuxd binary (FR-qmuxd-40).
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 removes the libc dependency, so the binary runs in a static image.
RUN CGO_ENABLED=0 go build -trimpath -o /out/qmuxd ./cmd/qmuxd

# The static distroless image has no shell and no package manager. Its nonroot tag
# runs as the user 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/qmuxd /usr/local/bin/qmuxd
USER nonroot:nonroot
# Mount the configuration file and the signing key at /etc/qmuxd (FR-qmuxd-41).
EXPOSE 53/udp 53/tcp
ENTRYPOINT ["/usr/local/bin/qmuxd"]
CMD ["serve", "--config", "/etc/qmuxd/qmuxd.json", "--listen", "0.0.0.0:53"]

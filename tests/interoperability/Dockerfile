# This image holds qmuxctl and qmux-probe for the keygen step and the probe service of
# compose.yaml. The build context is the repository root.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/qmuxctl ./cmd/qmux-probe && mkdir /out/shared

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/qmuxctl /out/qmux-probe /usr/local/bin/
# An empty named volume takes the owner of the directory that it first mounts on.
# The keygen step mounts the shared volume first, so the user 65532 of qmuxd owns it.
COPY --from=build --chown=nonroot:nonroot /out/shared /shared
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/qmux-probe"]

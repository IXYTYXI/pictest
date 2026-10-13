FROM golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/upic-server ./cmd/upic-server
RUN mkdir -p /data && chown 10001:10001 /data

FROM scratch
COPY --from=builder /out/upic-server /upic-server
COPY --from=builder --chown=10001:10001 /data /data
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/upic-server"]

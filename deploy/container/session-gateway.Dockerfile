# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/session-gateway ./apps/session-gateway
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/session-gateway ./apps/session-gateway

FROM alpine:3.22
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/session-gateway /session-gateway
COPY --from=build /go/pkg/mod/github.com/coder/websocket@v1.8.15/LICENSE.txt /usr/share/licenses/session-gateway/websocket-LICENSE.txt
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/session-gateway"]

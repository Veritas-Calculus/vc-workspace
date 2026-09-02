# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/mcp ./apps/mcp
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mcp ./apps/mcp

FROM alpine:3.22
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcp /mcp
USER 65532:65532
EXPOSE 8090
ENTRYPOINT ["/mcp"]

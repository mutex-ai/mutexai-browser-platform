FROM golang:1.26.1-alpine AS builder
WORKDIR /app
ENV GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY src ./src
RUN CGO_ENABLED=0 go build -trimpath -o /browser-platform ./src/cmd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && addgroup -S platform && adduser -S -G platform -u 10001 platform
COPY --from=builder /browser-platform /usr/local/bin/browser-platform
USER 10001
ENTRYPOINT ["/usr/local/bin/browser-platform"]

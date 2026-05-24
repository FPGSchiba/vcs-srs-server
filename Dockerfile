FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -tags production,headless \
    -trimpath \
    -ldflags="-w -s" \
    -o /vcs-srs-server \
    .

FROM gcr.io/distroless/static-debian12
COPY --from=builder /vcs-srs-server /vcs-srs-server
EXPOSE 14446 14448 5002/udp
ENTRYPOINT ["/vcs-srs-server"]

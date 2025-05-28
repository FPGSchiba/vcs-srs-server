# Build the application from source
FROM golang:1.24 AS build-stage

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Copy all .go files recursively, excluding test files
COPY . .
RUN find . -type f -name "*_test.go" -delete

# Headless build for production
RUN CGO_ENABLED=0 GOOS=linux go build -tags desktop,production -ldflags "-w -s" -o build/bin/vcs-server-headless
RUN mkdir /dist
RUN mv /app/build/bin/vcs-server /dist/vcs-server

# Deploy the application binary into a lean image
FROM gcr.io/distroless/base-debian11:latest AS build-release-stage

WORKDIR /

COPY --from=build-stage /dist/vcs-server /usr/bin/vcs-server

COPY example.config.yaml /etc/vcs-server/config.yaml

ENV GIN_MODE=release

USER nonroot:nonroot

ENTRYPOINT ["/usr/bin/vcs-server", "--config=/etc/vcs-server/config.yaml", "--autostart"]
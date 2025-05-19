# Build the application from source
FROM golang:1.24 AS build-stage

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Copy all .go files recursively, excluding test files
COPY . .
RUN find . -type f -name "*_test.go" -delete

# Install NodeJS & Wails CLI
RUN apt update
RUN apt-get -y install nodejs npm libgtk-3-dev libwebkit2gtk-4.0-dev
RUN go install github.com/wailsapp/wails/v2/cmd/wails@latest

RUN CGO_ENABLED=0 GOOS=linux wails build -clean -nocolour -o vcs-server
RUN mkdir /dist
RUN mv /app/build/bin/vcs-server /dist/vcs-server

# Deploy the application binary into a lean image
FROM gcr.io/distroless/base-debian11 AS build-release-stage

WORKDIR /

COPY --from=build-stage /dist/vcs-server /usr/bin/vcs-server

COPY example.config.yaml /etc/vcs-server/config.yaml

ENV GIN_MODE=release

USER nonroot:nonroot

ENTRYPOINT ["/usr/bin/vcs-server", "--config=etc/vcs-server/config.yaml"]
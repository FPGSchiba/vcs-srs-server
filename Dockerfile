# Build the application from source
FROM golang:1.24 AS build-stage

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Install dependencies
RUN apt update && apt-get -y install gcc libgtk-3-dev libwebkit2gtk-4.1-dev webkit2gtk-4.1

# Copy all .go files recursively, excluding test files
COPY . .
RUN find . -type f -name "*_test.go" -delete

# Headless build for production
RUN GOOS=linux go build -tags production,headless -trimpath -buildvcs=false -ldflags="-w -s" -o bin/vcs-server-headless
RUN mkdir /dist
RUN mv /app/bin/vcs-server-headless /dist/vcs-server

# Create lib directory and copy required libraries
# Copy only non-core libraries
RUN mkdir -p /dist/lib
RUN ldd /dist/vcs-server \
     | awk '/=>/ && $3 !~ /libc\.so/ && $3 !~ /ld-linux/ {print $3}' \
     | xargs -I {} cp -v {} /dist/lib/ || true

RUN echo '[]' > /dist/banned_clients.json

FROM gcr.io/distroless/base-debian12:latest AS build-release-stage

WORKDIR /

COPY --from=build-stage /dist/vcs-server /usr/bin/vcs-server
COPY --from=build-stage /dist/lib /usr/lib/
COPY --from=build-stage /dist/banned_clients.json /etc/vcs-server/banned_clients.json

ENV LD_LIBRARY_PATH=/usr/lib

COPY example.config.yaml /etc/vcs-server/config.yaml

USER nonroot:nonroot

ENTRYPOINT ["/usr/bin/vcs-server", "--config=/etc/vcs-server/config.yaml", "--banned=/etc/vcs-server/banned_clients.json", "--file-log=false"]
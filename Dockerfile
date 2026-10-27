FROM golang:1.27.3 AS builder
ARG TARGETARCH

# protoc v36.2 from the upstream release, verified by sha256 per architecture.
# Keep in step with .github/actions/generate/action.yml
RUN apt-get update && apt-get install -y --no-install-recommends unzip && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in \
         amd64) protoc_arch=linux-x86_64;  protoc_sha=121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278 ;; \
         arm64) protoc_arch=linux-aarch_64; protoc_sha=8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a ;; \
         *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
       esac \
    && curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/protoc.zip \
         "https://github.com/protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-${protoc_arch}.zip" \
    && echo "${protoc_sha}  /tmp/protoc.zip" | sha256sum -c - \
    && unzip -o /tmp/protoc.zip -d /usr/local bin/protoc 'include/*' \
    && rm /tmp/protoc.zip \
    && protoc --version

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# protoc-gen-go is derived from go.mod; protoc-gen-go-grpc ships from its own
# nested module in grpc-go, so it cannot be derived and is pinned by hand.
# Keep in step with .github/actions/generate/action.yml
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf) \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

COPY . .

RUN mkdir -p srspb voicecontrolpb vcsauthpb \
    && protoc --go_out=./srspb --go_opt=paths=source_relative \
              --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative \
              srs.proto \
    && protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative \
              --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative \
              control.proto \
    && protoc --go_out=./vcsauthpb --go_opt=paths=source_relative \
              --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative \
              vcs-auth-plugin.proto

RUN go run github.com/99designs/gqlgen@$(go list -m -f '{{.Version}}' github.com/99designs/gqlgen) generate

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

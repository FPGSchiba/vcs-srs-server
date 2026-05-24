FROM golang:1.25 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends protobuf-compiler && rm -rf /var/lib/apt/lists/*

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest \
    && go install github.com/99designs/gqlgen@v0.17.89

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

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

RUN gqlgen generate

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

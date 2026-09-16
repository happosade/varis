FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /archive-core ./cmd/archive-core

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    par2 xorriso wodim ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /archive-core /usr/local/bin/archive-core
ENTRYPOINT ["/usr/local/bin/archive-core"]

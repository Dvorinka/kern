# kern-runner image. The agent shells out to `docker`, so the image carries the
# Docker CLI only — mount the host socket (-v /var/run/docker.sock:...) at run.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/kern-runner ./cmd/runner

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git docker.io \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kern-runner /usr/local/bin/kern-runner
ENTRYPOINT ["kern-runner"]

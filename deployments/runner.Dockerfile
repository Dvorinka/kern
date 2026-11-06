# kern-runner image. The agent shells out to `docker`, so the image carries the
# Docker CLI only — mount the host socket (-v /var/run/docker.sock:...) at run.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/dvorinka/kern/internal/version.Version=${VERSION}" \
    -o /out/kern-runner ./cmd/runner

FROM debian:bookworm-slim
# docker.io on bookworm speaks API 1.41; dockerd 25+ wants >= 1.44. Ship the
# upstream static CLI instead.
ARG DOCKER_CLI=27.5.1
ARG DOCKER_COMPOSE=v2.40.3
ARG DOCKER_BUILDX=v0.20.0
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git curl \
    && curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_CLI}.tgz" \
       | tar xz -C /usr/local/bin --strip-components=1 docker/docker \
    && mkdir -p /usr/libexec/docker/cli-plugins \
    && curl -fsSL "https://github.com/docker/compose/releases/download/${DOCKER_COMPOSE}/docker-compose-linux-x86_64" \
       -o /usr/libexec/docker/cli-plugins/docker-compose \
    && curl -fsSL "https://github.com/docker/buildx/releases/download/${DOCKER_BUILDX}/buildx-${DOCKER_BUILDX}.linux-amd64" \
       -o /usr/libexec/docker/cli-plugins/docker-buildx \
    && chmod +x /usr/libexec/docker/cli-plugins/* \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kern-runner /usr/local/bin/kern-runner
ENTRYPOINT ["kern-runner"]

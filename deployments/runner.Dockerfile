# kern-runner image. The agent shells out to `docker`, so the image carries the
# Docker CLI only — mount the host socket (-v /var/run/docker.sock:...) at run.
FROM golang:1.27-bookworm AS build
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
    && dl() { repo="$1"; tag="$2"; asset="$3"; dest="$4"; \
         curl -fsSL --retry 8 --retry-all-errors --retry-delay 4 -C - \
           "https://github.com/${repo}/releases/download/${tag}/${asset}" -o "$dest" \
         || { aid=$(curl -fsSL "https://api.github.com/repos/${repo}/releases/tags/${tag}" \
               | awk -v want="\"name\": \"${asset}\"" \
                 '/"id":/ { match($0, /[0-9]+/); last=substr($0, RSTART, RLENGTH) } \
                  index($0, want) { print last; exit }'); \
              test -n "$aid" \
              && curl -fsSL --retry 8 --retry-all-errors --retry-delay 4 -C - \
                 -H 'Accept: application/octet-stream' \
                 "https://api.github.com/repos/${repo}/releases/assets/${aid}" -o "$dest"; }; } \
    && dl docker/compose "${DOCKER_COMPOSE}" docker-compose-linux-x86_64 \
         /usr/libexec/docker/cli-plugins/docker-compose \
    && dl docker/buildx "${DOCKER_BUILDX}" "buildx-${DOCKER_BUILDX}.linux-amd64" \
         /usr/libexec/docker/cli-plugins/docker-buildx \
    && chmod +x /usr/libexec/docker/cli-plugins/* \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kern-runner /usr/local/bin/kern-runner
ENTRYPOINT ["kern-runner"]

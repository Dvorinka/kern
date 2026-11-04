# kern-runner image. The agent shells out to `docker`, so the image carries the
# Docker CLI only — mount the host socket (-v /var/run/docker.sock:...) at run.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/kern-runner ./cmd/runner

FROM debian:bookworm-slim
# docker.io on bookworm speaks API 1.41; dockerd 25+ wants >= 1.44. Ship the
# upstream static CLI instead.
ARG DOCKER_CLI=27.5.1
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git curl \
    && curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_CLI}.tgz" \
       | tar xz -C /usr/local/bin --strip-components=1 docker/docker \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kern-runner /usr/local/bin/kern-runner
ENTRYPOINT ["kern-runner"]

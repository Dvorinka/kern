ARG GOLANG=1.27
FROM golang:${GOLANG}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/dvorinka/kern/internal/version.Version=${VERSION}" \
    -o /out/kern-api ./cmd/api

FROM node:22-alpine AS web
WORKDIR /web
RUN corepack enable
COPY apps/web/package.json apps/web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY apps/web/ ./
RUN pnpm build

FROM alpine:3.24
RUN addgroup -S kern && adduser -S kern -G kern && mkdir -p /data/blobs && chown -R kern:kern /data
COPY --from=build /out/kern-api /usr/local/bin/kern-api
COPY --from=web /web/dist /app/web
ENV KERN_WEB_DIR=/app/web
USER kern
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kern-api"]

ARG GOLANG=1.24
FROM golang:${GOLANG}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -o /out/kern-api ./cmd/api

FROM alpine:3.20
RUN addgroup -S kern && adduser -S kern -G kern && mkdir -p /data/blobs && chown -R kern:kern /data
COPY --from=build /out/kern-api /usr/local/bin/kern-api
USER kern
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kern-api"]

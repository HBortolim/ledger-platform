FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/root/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -o /projection-service ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache curl
COPY --from=build /projection-service /projection-service
EXPOSE 8082
ENTRYPOINT ["/projection-service"]

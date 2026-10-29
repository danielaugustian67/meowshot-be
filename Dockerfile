FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/meowshot ./cmd/api

# Alpine keeps a shell in the image for `docker compose exec api sh` debugging on the VPS.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H meowshot
COPY --from=build /out/meowshot /usr/local/bin/meowshot
USER meowshot
EXPOSE 8080
CMD ["meowshot"]

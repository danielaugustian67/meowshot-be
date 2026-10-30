# Base images come from the team registry on the VPS. Elsewhere (CI, a laptop), build with
#   docker build --build-arg REGISTRY= .
# to pull the same tag from Docker Hub.
ARG REGISTRY=10.3.11.234:5000/

FROM ${REGISTRY}golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nodynamic keeps the HEIC/WebP decoders on their built-in WASM path instead of loading system
# libraries through purego, which would make the binary dynamically linked and unrunnable on scratch.
RUN CGO_ENABLED=0 go build -trimpath -tags nodynamic -ldflags="-s -w" -o /out/meowshot ./cmd/api

# No base image: the static binary only needs CA certificates for B2, SMTP and Google.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/meowshot /meowshot
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/meowshot"]

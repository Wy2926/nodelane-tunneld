FROM node:24-alpine AS frontend-build
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10.33.2 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
RUN apk add --no-cache tar zip
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-build /src/web/dist ./internal/server/assets/web
RUN sh ./deploy/build-client-assets.sh "$VERSION" /out/releases
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" -o /out/tunneld ./cmd/tunneld

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="NodeLane Tunnel" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.source="https://github.com/Wy2926/nodelane-tunneld"
COPY --from=build /out/tunneld /tunneld
COPY --from=build /out/releases /releases
USER nonroot:nonroot
ENV RELEASE_DIR=/releases
EXPOSE 9000
ENTRYPOINT ["/tunneld"]

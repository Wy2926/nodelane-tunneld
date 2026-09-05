FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend-build
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10.33.2 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go-build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

# Keep client releases independent from control-plane image releases. This
# stage only receives nt inputs, so server and website changes retain the exact
# cached client packages when client-version.txt is unchanged.
FROM go-build-base AS client-build
ARG NT_VERSION
RUN apk add --no-cache tar zip
COPY client-version.txt ./
COPY deploy/build-client-assets.sh ./deploy/build-client-assets.sh
COPY cmd/nt ./cmd/nt
COPY internal/client ./internal/client
RUN sed -i 's/\r$//' ./deploy/build-client-assets.sh && \
    nt_version="$NT_VERSION" && \
    if [ -z "$nt_version" ]; then nt_version="$(tr -d '\r\n' < client-version.txt)"; fi && \
    sh ./deploy/build-client-assets.sh "$nt_version" /out/releases

FROM go-build-base AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
COPY . .
COPY --from=frontend-build /src/web/dist ./internal/server/assets/web
RUN sed -i 's/\r$//' ./internal/server/assets/run.sh && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" -o /out/tunneld ./cmd/tunneld

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="NodeLane Tunnel" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.source="https://github.com/Wy2926/nodelane-tunneld"
COPY --from=build /out/tunneld /tunneld
COPY --from=client-build /out/releases /releases
USER nonroot:nonroot
ENV RELEASE_DIR=/releases
EXPOSE 9000
ENTRYPOINT ["/tunneld"]

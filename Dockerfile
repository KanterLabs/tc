# Build the browser bundle in the same reproducible sequence used by CI.  The
# generated files are copied into the Go embed tree before the server build.
FROM node:22.23.2-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5 AS frontend

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
COPY openapi.yaml /src/openapi.yaml
COPY internal/httpapi/openapi.json /src/internal/httpapi/openapi.json
RUN npm run openapi:check
RUN npm run check
RUN npm test
RUN npm run build

FROM golang:1.25.14-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend /src/web/dist ./internal/webassets/dist
# Keep the contract's compatibility path populated as well. The current Go
# asset loader imports internal/webassets; older build tooling expects the
# internal/frontend path, and having both prevents a stale embedded UI.
COPY --from=frontend /src/web/dist ./internal/frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/roadmap ./cmd/roadmap \
    && install -d -m 0750 -o 65532 -g 65532 /out/data

# Keep the runtime small and unprivileged. The static binary and Go-native
# healthcheck make a shell and package manager unnecessary. Copy the CA bundle
# from the existing digest-pinned Debian build stage so Cloudflare JWKS HTTPS
# verification remains available in scratch.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/roadmap /usr/local/bin/roadmap
COPY --from=build --chown=65532:65532 /out/data /data

WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
ENV ROADMAP_ADDR=0.0.0.0:8080 \
    ROADMAP_DB=/data/roadmap.db \
    ROADMAP_PUBLIC_ORIGIN=http://localhost:8080 \
    ROADMAP_ADMIN_EMAIL= \
    ROADMAP_SECURE_COOKIES=true \
    ROADMAP_DEMO_SEED=false \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=5 \
    CMD ["/usr/local/bin/roadmap", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/roadmap"]

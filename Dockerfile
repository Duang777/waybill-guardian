FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./

ARG AMAP_BROWSER_ID=""
ARG AMAP_BROWSER_CODE=""
ARG VITE_VECTOR_MAP=""
RUN VITE_AMAP_KEY="${AMAP_BROWSER_ID}" \
    VITE_AMAP_SECURITY_JS_CODE="${AMAP_BROWSER_CODE}" \
    VITE_VECTOR_MAP="${VITE_VECTOR_MAP}" \
    npm run build
RUN npx --yes license-checker-rseidelsohn@4.4.2 \
    --production \
    --excludePrivatePackages \
    --files /out/licenses/web >/dev/null

FROM golang:1.26-alpine AS server-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN GOBIN=/tools go install github.com/google/go-licenses/v2@v2.0.1
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY LICENSE ./
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/waybill-guardian ./cmd/server \
    && mkdir -p /out/data \
    && chown 65532:65532 /out/data
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    /tools/go-licenses save ./cmd/server \
    --ignore github.com/dop251/goja/ftoa \
    --save_path=/out/licenses/go \
    && goja_dir="$(go list -m -f '{{.Dir}}' github.com/dop251/goja)" \
    && mkdir -p /out/licenses/go/github.com/dop251/goja/ftoa \
    && cp "${goja_dir}/ftoa/LICENSE_LUCENE" \
    /out/licenses/go/github.com/dop251/goja/ftoa/LICENSE_LUCENE

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=server-build /out/waybill-guardian /app/waybill-guardian
COPY --from=web-build /src/web/dist /app/web
COPY --chown=65532:65532 --from=server-build /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY docs/licenses/ /app/docs/licenses/
COPY --from=server-build /out/licenses/go /app/third_party_licenses/go
COPY --from=web-build /out/licenses/web /app/third_party_licenses/web

ENV HTTP_ADDR=0.0.0.0:8080
ENV WEB_STATIC_DIR=/app/web
ENV ALLOW_NON_LOOPBACK_LOCAL=true
ENV LOCAL_TRUSTED_REMOTE=container-gateway
ENV AGENT_MODE=online
ENV PLATFORM=mock
ENV STORAGE=jsonl
ENV DATA_DIR=/data

EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/app/waybill-guardian"]

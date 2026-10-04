FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./

ARG AMAP_BROWSER_ID=""
ARG AMAP_BROWSER_CODE=""
RUN VITE_AMAP_KEY="${AMAP_BROWSER_ID}" \
    VITE_AMAP_SECURITY_JS_CODE="${AMAP_BROWSER_CODE}" \
    npm run build

FROM golang:1.25-alpine AS server-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/waybill-guardian ./cmd/server \
    && mkdir -p /out/data \
    && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=server-build /out/waybill-guardian /app/waybill-guardian
COPY --from=web-build /src/web/dist /app/web
COPY --chown=65532:65532 --from=server-build /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY docs/licenses/ /app/licenses/

ENV HTTP_ADDR=127.0.0.1:8080
ENV WEB_STATIC_DIR=/app/web
ENV AGENT_MODE=demo
ENV PLATFORM=mock
ENV STORAGE=jsonl
ENV DATA_DIR=/data

EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/app/waybill-guardian"]

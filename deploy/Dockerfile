# Build from the repository root:  docker build -f deploy/Dockerfile -t webssh .

# The in-browser SSH client for end-to-end sessions (WebAssembly).
FROM golang:1.26-alpine AS wasm
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN mkdir /out && GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ssh.wasm ./cmd/webssh-wasm  && cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" /out/

FROM node:24-alpine AS web
WORKDIR /src/apps/web
COPY apps/web/package.json apps/web/package-lock.json ./
RUN npm ci
COPY apps/web/ ./
COPY --from=wasm /out/ ./public/wasm/
RUN mkdir -p /src/server/internal/web/dist && WEBSSH_PREBUILT_WASM=1 npm run build

FROM golang:1.26-alpine AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/server/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webssh ./cmd/webssh

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/webssh /webssh
ENV WEBSSH_ADDR=0.0.0.0:7022 \
    WEBSSH_DATA_DIR=/data \
    WEBSSH_TRUST_PROXY=1
VOLUME /data
EXPOSE 7022
USER nonroot:nonroot
ENTRYPOINT ["/webssh"]

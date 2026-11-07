# Stage 1: Build & Pre-compress Frontend Assets
FROM --platform=$BUILDPLATFORM node:20-alpine AS frontend-builder
WORKDIR /workspace/frontend
COPY frontend/package*.json ./
RUN npm ci || npm install
COPY frontend/ ./
RUN npm run build && node -e "const fs=require('fs'),path=require('path'),zlib=require('zlib');const exts=new Set(['.js','.mjs','.css','.html','.svg','.json','.ttf','.webmanifest']);function walk(d){for(const e of fs.readdirSync(d,{withFileTypes:true})){const p=path.join(d,e.name);if(e.isDirectory()){walk(p);continue;}if(e.name.endsWith('.map')){fs.rmSync(p);continue;}const buf=fs.readFileSync(p);if(exts.has(path.extname(e.name).toLowerCase())&&buf.length>256){const gz=zlib.gzipSync(buf,{level:9});if(gz.length<buf.length){fs.writeFileSync(p+'.gz',gz);fs.rmSync(p);}}}}walk('dist');"

# Stage 2: Cross-Compile Pure-Go Server Binary with Embedded Frontend
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS backend-builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=1.0.1
ARG CHANNEL=release
ARG GIT_COMMIT=release
WORKDIR /workspace/backend
ENV CGO_ENABLED=0
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
COPY CHANGELOG*.md ./internal/version/
COPY --from=frontend-builder /workspace/frontend/dist ./webdist/dist
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X dbharbor/backend/internal/version.Version=${VERSION} -X dbharbor/backend/internal/version.Channel=${CHANNEL} -X dbharbor/backend/internal/version.GitCommit=${GIT_COMMIT} -X dbharbor/backend/internal/version.Edition=server" \
    -o /out/dbharbor ./cmd/dbharbor

# Stage 3: Minimal Non-Root Runtime Image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S dbharbor \
    && adduser -S -G dbharbor dbharbor \
    && mkdir -p /app/data \
    && chown -R dbharbor:dbharbor /app
WORKDIR /app
COPY --from=backend-builder /out/dbharbor /app/dbharbor
COPY LICENSE /app/LICENSE
USER dbharbor
VOLUME ["/app/data"]
EXPOSE 8787
ENV DBHARBOR_PORT=8787 \
    DBHARBOR_DATA_DIR=/app/data
ENTRYPOINT ["/app/dbharbor"]

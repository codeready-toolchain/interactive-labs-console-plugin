FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM --platform=${BUILDPLATFORM} golang:1.27-alpine AS backend
ARG TARGETOS
ARG TARGETARCH
ARG COMMIT=unknown
ARG BUILDTIME=unknown

WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
RUN echo "Building the binary for $TARGETOS/$TARGETARCH with commit $COMMIT and buildtime $BUILDTIME"
RUN cd backend && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -ldflags "-X main.commit=${COMMIT} -X main.buildtime=${BUILDTIME}" -o /out/interactive-labs ./cmd/server

FROM --platform=${TARGETPLATFORM} alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 interactive-labs && \
  mkdir -p /var/lib/interactive-labs/cache && \
  chown 0:0 /var/lib/interactive-labs/cache && \
  chmod 0770 /var/lib/interactive-labs/cache
WORKDIR /app
COPY --from=frontend /src/frontend/dist ./frontend
COPY --from=backend /out/interactive-labs /usr/local/bin/interactive-labs
USER 10001
ENTRYPOINT ["/usr/local/bin/interactive-labs"]

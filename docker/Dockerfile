FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
RUN cd backend && CGO_ENABLED=0 go build -o /out/interactive-labs ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git && adduser -D -u 10001 interactive-labs && \
    mkdir -p /var/lib/interactive-labs/cache && \
    chown 0:0 /var/lib/interactive-labs/cache && \
    chmod 0770 /var/lib/interactive-labs/cache
WORKDIR /app
COPY --from=frontend /src/frontend/dist ./frontend
COPY --from=backend /out/interactive-labs /usr/local/bin/interactive-labs
USER 10001
ENTRYPOINT ["/usr/local/bin/interactive-labs"]

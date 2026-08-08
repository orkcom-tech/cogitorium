# Stage 1 — build the web UI
FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2 — build the Go binary with the UI embedded
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/cogitorium ./cmd/cogitorium

# Stage 3 — runtime
FROM alpine:3.21
# /data is created with the runtime user as owner so a fresh named volume
# inherits writable permissions.
RUN adduser -D -H cogitorium && mkdir /data && chown cogitorium:cogitorium /data
COPY --from=build /out/cogitorium /usr/local/bin/cogitorium
USER cogitorium
VOLUME /data
EXPOSE 8688
ENTRYPOINT ["cogitorium", "serve", "--listen", "0.0.0.0:8688", "--data", "/data"]

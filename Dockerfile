# Stage 1: build the React frontend.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: build a static, CGO-free Go binary with the frontend embedded.
# SQLite (with encryption) is pure Go via ncruces/go-sqlite3, so no C toolchain.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN go test ./... \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/finance-tracker ./cmd/server

# Stage 3: minimal runtime, no shell or package manager, runs as non-root (65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/finance-tracker /finance-tracker
ENV FIN_TRACKER_DATA_DIR=/data
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/finance-tracker"]

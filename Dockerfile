# --- web UI ---
FROM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY internal/vault/testdata /src/internal/vault/testdata
RUN npm run typecheck && npm test && npm run build

# --- server ---
FROM golang:1.27-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /restash-server ./cmd/restash-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /restash-server /restash-server
ENV RESTASH_STORAGE=/storage
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/restash-server"]

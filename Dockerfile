FROM golang:1.27-alpine AS go
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /restash-server ./cmd/restash-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /restash-server /restash-server
ENV RESTASH_STORAGE=/storage
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/restash-server"]

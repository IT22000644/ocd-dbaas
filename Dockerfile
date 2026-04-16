# syntax=docker/dockerfile:1.6

FROM golang:1.20-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/dbaas-controller .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/dbaas-controller /usr/local/bin/dbaas-controller
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/dbaas-controller"]

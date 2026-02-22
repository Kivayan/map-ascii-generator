FROM golang:1.24-alpine AS build

WORKDIR /src

COPY api/go.mod api/go.sum ./
RUN go mod download

COPY api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

EXPOSE 8081

ENTRYPOINT ["/api"]

FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/checkout/*.go ./cmd/checkout/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /app ./cmd/checkout

FROM scratch
COPY --from=build /app /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]

# Build: binários estáticos, sem CGO, sem símbolos de debug.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sre-agent ./cmd/sre-agent

# Runtime: distroless, sem shell nem gerenciador de pacotes, usuário não-root.
# Um binário, vários subcomandos: intake (padrão), job, migrate, review e import-postmortems.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
USER nonroot:nonroot
ENTRYPOINT ["/app/sre-agent"]
CMD ["intake"]

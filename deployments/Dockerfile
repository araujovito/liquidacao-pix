# Uma imagem com os quatro programas (migrar, api, relay, worker); o compose
# escolhe qual rodar. Binários estáticos sobre distroless: sem shell nem
# gerenciador de pacotes na imagem final, e usuário não-root.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/migrar ./cmd/api ./cmd/relay ./cmd/worker ./cmd/semear

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
USER nonroot
ENTRYPOINT []

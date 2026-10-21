COMPOSE := docker compose -f deployments/docker-compose.yml

# Credenciais só de desenvolvimento (as do docker-compose). Em outro ambiente,
# defina DATABASE_URL antes de chamar o make.
export DATABASE_URL ?= postgres://pix:pix@localhost:5432/pix?sslmode=disable

.PHONY: infra infra-parar migrar semear api teste teste-unidade lint fmt

infra: ## sobe Postgres e Redis e espera ficarem saudáveis
	$(COMPOSE) up -d --wait postgres redis

infra-parar:
	$(COMPOSE) down

migrar: infra
	go run ./cmd/migrar

semear: migrar ## cria cliente-0001@pix … cliente-0100@pix com R$ 10.000 cada
	go run ./cmd/semear -contas 100 -saldo 1000000

api: migrar
	go run ./cmd/api

teste: ## todos os testes; os de integração sobem contêineres próprios
	go test -race -count=1 ./...

teste-unidade: ## só os testes que não precisam de Docker
	go test -race -count=1 -short ./...

lint:
	gofmt -l . | (! grep .)
	go vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .

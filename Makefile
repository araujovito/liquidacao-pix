COMPOSE := docker compose -f deployments/docker-compose.yml

.PHONY: infra infra-parar migrar teste teste-unidade lint fmt

infra: ## sobe Postgres e Redis e espera ficarem saudáveis
	$(COMPOSE) up -d --wait postgres redis

infra-parar:
	$(COMPOSE) down

migrar: infra
	go run ./cmd/migrar

teste: ## todos os testes; os de integração sobem contêineres próprios
	go test -race -count=1 ./...

teste-unidade: ## só os testes que não precisam de Docker
	go test -race -count=1 -short ./...

lint:
	gofmt -l . | (! grep .)
	go vet ./...

fmt:
	gofmt -w .

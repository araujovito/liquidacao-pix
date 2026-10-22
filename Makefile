COMPOSE := docker compose -f deployments/docker-compose.yml

# Credenciais só de desenvolvimento (as do docker-compose). Em outro ambiente,
# defina DATABASE_URL antes de chamar o make.
export DATABASE_URL ?= postgres://pix:pix@localhost:5432/pix?sslmode=disable
export REDIS_URL ?= redis://localhost:6379/0

.PHONY: testar-alertas observabilidade caos carga-pico carga-sustentada infra sistema infra-parar migrar semear api relay worker teste teste-unidade lint fmt

infra: ## sobe Postgres e Redis e espera ficarem saudáveis
	$(COMPOSE) up -d --wait postgres redis

sistema: ## tudo em contêineres: postgres, redis, migrar, api, relay, worker
	$(COMPOSE) --profile sistema up -d --build --wait

testar-alertas: ## testa as regras de alerta com séries sintéticas (promtool, sem subir o sistema)
	docker run --rm --entrypoint promtool -v "$(CURDIR)/deployments/observabilidade:/regras:ro" -w /regras prom/prometheus:v3.5.0 test rules alertas_test.yml

observabilidade: ## sistema + Prometheus (:9090), Grafana (:3001) e Jaeger (:16686), com rastreamento ligado
	OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318 $(COMPOSE) --profile sistema --profile observabilidade up -d --build --wait

infra-parar:
	$(COMPOSE) --profile sistema --profile observabilidade down

migrar: infra
	go run ./cmd/migrar

semear: migrar ## cria cliente-0001@pix … cliente-0100@pix com R$ 10.000 cada
	go run ./cmd/semear -contas 100 -saldo 1000000

api: migrar
	go run ./cmd/api

relay: migrar
	go run ./cmd/relay

worker: migrar
	go run ./cmd/worker

caos: ## teste de caos contra o sistema em contêineres (~2 min)
	$(MAKE) sistema
	go run ./cmd/semear -contas 100 -saldo 1000000
	go run ./tests/caos

carga-pico: ## k6: sobe até PICO pedidos/s (padrão 4000); relatório em tests/carga/resultados
	tests/carga/rodar.sh pico

carga-sustentada: ## k6: TAXA pedidos/s (padrão 1500) por DURACAO (padrão 3m)
	tests/carga/rodar.sh sustentada

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

.PHONY: build web test e2e run
build: web
	go build -o bin/falcon ./cmd/falcon
web:
	cd web && npm ci --no-audit --no-fund && npm run build
test:
	go vet ./... && go test ./...
e2e:
	cd web && npx playwright test
run: build
	./bin/falcon -open

.PHONY: tidy build run demo test test-integration vet
tidy:           ## download dependencies and write go.sum
	go mod tidy
build: tidy     ## build ./bin/dbdeck
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/dbdeck ./cmd/dbdeck
run:            ## make run URL=postgres://user:pass@localhost/db   (or a sqlite file)
	go run ./cmd/dbdeck $(URL)
demo:           ## open the built-in demo database
	go run ./cmd/dbdeck --demo
vet:
	go vet ./...
test:           ## unit + flow tests (no servers needed; engine integration tests skip themselves)
	go test ./...
test-integration: ## needs scratch databases, see README "Testing"
	dbdeck_TEST_POSTGRES="$(PG)" dbdeck_TEST_MYSQL="$(MYSQL)" go test -count=1 ./internal/driver/...

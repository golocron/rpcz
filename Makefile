BASE_PATH := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
MKFILE_PATH := $(BASE_PATH)/Makefile
COVER_OUT := cover.out
BENCH_OUT := results.bench

.DEFAULT_GOAL := help

test: ## Run tests
	go test ./... -coverprofile=$(COVER_OUT) -v

bench: ## Run benchmarks
	go test -benchmem -benchtime 5s -bench=Benchmark -count 5 -timeout 900s -cpu=8

bench-results: ## Run benchmarks and save the output
	go test -benchmem -benchtime 5s -bench=Benchmark -count 5 -timeout 600s -cpu=8 | tee $(BENCH_OUT)

cover: ## Show test coverage
	@if [ -f $(COVER_OUT) ]; then \
		go tool cover -func=$(COVER_OUT); \
		rm -f $(COVER_OUT); \
	else \
		echo "$(COVER_OUT) is missing. Please run 'make test'"; \
	fi

results: ## Show benchmark results
	@if [ -f $(BENCH_OUT) ]; then \
		benchstat $(BENCH_OUT); \
	else \
		echo "$(BENCH_OUT) is missing. Please run 'make bench-results'"; \
	fi

clean: ## Remove binaries
	@rm -f $(COVER_OUT)
	@rm -f $(BENCH_OUT)
	@find $(BASE_PATH) -name ".DS_Store" -depth -exec rm {} \;

help: ## Show help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: test bench cover clean help

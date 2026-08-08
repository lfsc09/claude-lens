main_package_path = ./cmd/claude-lens
binary_name = claude-lens
build_dir = bin

.PHONY: help
help:
	@echo "Usage: make [target]"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## install-hooks: Install Git hooks for the project (Development only)
.PHONY: install-hooks
install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/post-commit

## build: Build the project for the current platform
.PHONY: build
build:
	@mkdir -p ${build_dir}
	go build -o ${build_dir}/${binary_name} ${main_package_path}

## build-all: Build the project for all platforms (Linux, macOS, Windows)
.PHONY: build-all
build-all:
	@mkdir -p ${build_dir}
	GOARCH=amd64 GOOS=linux go build -o ${build_dir}/${binary_name} ${main_package_path}
	GOARCH=amd64 GOOS=darwin go build -o ${build_dir}/${binary_name}-darwin ${main_package_path}
	GOARCH=amd64 GOOS=windows go build -o ${build_dir}/${binary_name}-windows.exe ${main_package_path}

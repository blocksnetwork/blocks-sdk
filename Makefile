.PHONY: setup build test lint clean \
       release-node release-python release-cli \
       publish-node publish-python publish-cli

setup:  ## Unix/macOS only — see README.md for Windows notes
	npm install
	npm run build --workspace sdks/node
	python3 -m venv .venv
	.venv/bin/pip install -e sdks/python
	$(MAKE) -C cli build install
	@echo "Add ~/.blocks/bin to PATH if not already: export PATH=\"$$HOME/.blocks/bin:$$PATH\""

build:
	npm run build --workspace sdks/node
	$(MAKE) -C cli build

test:
	npm test --workspace sdks/node
	cd sdks/python && pip install -e ".[dev]" && pytest
	cd cli && go test ./...

lint:
	npm run lint --workspace sdks/node
	cd sdks/python && ruff check .
	cd cli && go vet ./...

# Release helpers — tag and push to trigger CI workflows.
# Usage:
#   make release-node VERSION=0.2.0      # → Artifactory
#   make release-python VERSION=0.2.0    # → Artifactory
#   make release-cli VERSION=0.2.0       # → Artifactory + GitHub Release
#   make publish-node VERSION=0.2.0      # → public npm
#   make publish-python VERSION=0.2.0    # → public PyPI
#   make publish-cli VERSION=0.2.0       # → public npm
# Append -rc to the version for a release candidate build:
#   make release-node VERSION=0.2.0-rc

define check-version
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION is required. Usage: make $@ VERSION=x.y.z"; \
		exit 1; \
	fi
endef

define check-master
	@if [ "$$(git rev-parse --abbrev-ref HEAD)" != "master" ]; then \
		echo "Warning: you are tagging from branch '$$(git rev-parse --abbrev-ref HEAD)', not master."; \
	fi
endef

release-node:
	$(check-version)
	$(check-master)
	git tag "node-v$(VERSION)" && git push origin "node-v$(VERSION)"

release-python:
	$(check-version)
	$(check-master)
	git tag "python-v$(VERSION)" && git push origin "python-v$(VERSION)"

release-cli:
	$(check-version)
	$(check-master)
	git tag "cli-v$(VERSION)" && git push origin "cli-v$(VERSION)"

publish-node:
	$(check-version)
	$(check-master)
	git tag "node-npm-v$(VERSION)" && git push origin "node-npm-v$(VERSION)"

publish-python:
	$(check-version)
	$(check-master)
	git tag "python-pypi-v$(VERSION)" && git push origin "python-pypi-v$(VERSION)"

publish-cli:
	$(check-version)
	$(check-master)
	git tag "cli-npm-v$(VERSION)" && git push origin "cli-npm-v$(VERSION)"

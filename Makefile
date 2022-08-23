# Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

VERSION             := $(shell cat VERSION)
REGISTRY            := eu.gcr.io/gardener-project/gardener
IMAGE_REPOSITORY    := $(REGISTRY)/vpa-exporter
IMAGE_TAG           := $(VERSION)
BUILD_DIR           := build
BIN_DIR             := bin

TOOLS_DIR := hack/tools
include hack/tools.mk

.PHONY: revendor
revendor:
	@GO111MODULE=on go mod tidy
	@GO111MODULE=on go mod vendor

.PHONY: build
build: 
	@.ci/build

.PHONY: docker-image
docker-image: 
	@docker build -t $(IMAGE_REPOSITORY):$(IMAGE_TAG) -f $(BUILD_DIR)/Dockerfile --rm .

.PHONY: docker-push
docker-push:
	@if ! docker images $(IMAGE_REPOSITORY) | awk '{ print $$2 }' | grep -q -F $(IMAGE_TAG); then echo "$(IMAGE_REPOSITORY) version $(IMAGE_TAG) is not yet built. Please run 'make docker-image'"; false; fi
	@docker push $(IMAGE_REPOSITORY):$(IMAGE_TAG)

.PHONY: clean
clean:
	@rm -rf $(BIN_DIR)/

.PHONY: check
check: $(GOLANGCI_LINT)
	@go vet ./cmd/...
	@go fmt ./cmd/...
	@hack/check.sh --golangci-lint-config=./.golangci.yaml ./cmd/...

.PHONY: test
test: $(GINKGO)
	@.ci/test

.PHONY: verify
verify: check test


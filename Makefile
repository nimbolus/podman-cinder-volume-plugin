.DEFAULT_GOAL := install

PLUGIN_NAME ?= akerouanton/cinder
PLUGIN_VERSION ?= devel
PLUGIN := $(PLUGIN_NAME):$(PLUGIN_VERSION)
ROOTFS := $(PWD)/plugin/rootfs/
TMP_IMAGE := cinder
TMP_CONTAINER := cinder-rootfs

include .env

.PHONY: clean
clean:
	rm -rf plugin/rootfs

.PHONY: build
build: clean
	docker build -t $(TMP_IMAGE) -f Dockerfile.build .
	-docker rm -f $(TMP_CONTAINER)
	docker create --name $(TMP_CONTAINER) $(TMP_IMAGE)
	docker cp $(TMP_CONTAINER):/ $(ROOTFS)

.PHONY: plugin
plugin: build
	-docker plugin disable -f $(PLUGIN)
	-docker plugin rm -f $(PLUGIN)
	docker plugin create $(PLUGIN) plugin/

.PHONY: install
install: plugin
	docker plugin set $(PLUGIN) \
		OS_AUTH_URL=$(OS_AUTH_URL) \
		OS_USERNAME=$(OS_USERNAME) \
		OS_USERID=$(OS_USERID) \
		OS_PASSWORD=$(OS_PASSWORD) \
		OS_PASSCODE=$(OS_PASSCODE) \
		OS_TENANT_ID=$(OS_TENANT_ID) \
		OS_TENANT_NAME=$(OS_TENANT_NAME) \
		OS_DOMAIN_ID=$(OS_DOMAIN_ID) \
		OS_DOMAIN_NAME=$(OS_DOMAIN_NAME) \
		OS_APPLICATION_CREDENTIAL_ID=$(OS_APPLICATION_CREDENTIAL_ID) \
		OS_APPLICATION_CREDENTIAL_NAME=$(OS_APPLICATION_CREDENTIAL_NAME) \
		OS_APPLICATION_CREDENTIAL_SECRET=$(OS_APPLICATION_CREDENTIAL_SECRET) \
		OS_REGION_NAME=$(OS_REGION_NAME) \
		LOG_LEVEL=$(LOG_LEVEL)
	docker plugin enable $(PLUGIN)

.PHONY: release
release: plugin
	docker plugin push $(PLUGIN)

.PHONY: lint
lint:
	docker run --rm -v $(PWD):/app -w /app golangci/golangci-lint:v1.24-alpine golangci-lint run -v

NAME = sidera
COMMIT = $(shell git rev-parse --short HEAD)
TAGS ?= $(shell cat release/DEFAULT_BUILD_TAGS_OTHERS)
LOW_MEMORY_TAGS ?= $(shell cat release/LOW_MEMORY_BUILD_TAGS)
GOTOOLCHAIN ?= go1.27.3
GOLANGCI_LINT_VERSION ?= v2.12.2
PROTOC_GEN_GO_VERSION ?= v1.36.11
PROTOC_GEN_GO_GRPC_VERSION ?= v1.5.1
GHR_REVISION ?= 610bfc9f5712eac7fe65a31fb1a214b64fb2b86c
export GOTOOLCHAIN

GOHOSTOS = $(shell go env GOHOSTOS)
GOHOSTARCH = $(shell go env GOHOSTARCH)
VERSION=$(shell CGO_ENABLED=0 GOOS=$(GOHOSTOS) GOARCH=$(GOHOSTARCH) go run ./cmd/internal/read_tag)

LDFLAGS_SHARED = $(shell cat release/LDFLAGS)
PARAMS = -v -trimpath -ldflags "-X 'github.com/Miku0139oao/sidera-core/constant.Version=$(VERSION)' $(LDFLAGS_SHARED) -s -w -buildid="
MAIN_PARAMS = $(PARAMS) -tags "$(TAGS)"
MAIN = ./cmd/sidera
PREFIX ?= $(shell go env GOPATH)
SING_FFI ?= sing-ffi
LIBBOX_FFI_CONFIG ?= ./experimental/libbox/ffi.json

.PHONY: build build_low_memory race ci_build ci_release_gate generate_completions schema \
	install fmt fmt_docs lint lint_install proto proto_install release release_repo \
	release_install test test_stdio docs docs_install clean

build:
	go build $(MAIN_PARAMS) $(MAIN)

build_low_memory:
	@mkdir -p dist
	CGO_ENABLED=0 go build -o dist/$(NAME)-low-memory $(PARAMS) -tags "$(LOW_MEMORY_TAGS)" $(MAIN)

race:
	go build -race $(MAIN_PARAMS) $(MAIN)

ci_build:
	go build $(PARAMS) $(MAIN) && \
	go build $(MAIN_PARAMS) $(MAIN)

ci_release_gate:
	bash script/ci-release-gate.sh all

generate_completions:
	go run -v --tags "$(TAGS),generate,generate_completions" $(MAIN)

schema:
	go run -ldflags "$(LDFLAGS_SHARED)" --tags "$(TAGS)" $(MAIN) schema -o docs/schema.json

install:
	go build -o $(PREFIX)/bin/$(NAME) $(MAIN_PARAMS) $(MAIN)

fmt:
	@golangci-lint fmt

fmt_docs:
	go run ./cmd/internal/format_docs

lint:
	GOOS=linux golangci-lint run ./...
	GOOS=android golangci-lint run ./...
	GOOS=windows golangci-lint run ./...
	GOOS=darwin golangci-lint run ./...
#	GOOS=freebsd golangci-lint run ./...

lint_install:
	go install -v github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

proto:
	@go run ./cmd/internal/protogen
	@gofumpt -l -w .
	@gofumpt -l -w .

proto_install:
	go install -v google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install -v google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

update_certificates:
	go run ./cmd/internal/update_certificates

release:
	@echo "release publication is CI-only; dispatch .github/workflows/build.yml from the release commit" >&2
	@false

release_repo: release

release_install:
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	git init -q "$$tmp/ghr"; \
	git -C "$$tmp/ghr" remote add origin https://github.com/nekohasekai/ghr.git; \
	git -C "$$tmp/ghr" fetch -q --depth=1 origin $(GHR_REVISION); \
	test "$$(git -C "$$tmp/ghr" rev-parse FETCH_HEAD)" = "$(GHR_REVISION)"; \
	git -C "$$tmp/ghr" checkout -q --detach FETCH_HEAD; \
	go -C "$$tmp/ghr" install -v .

update_android_version:
	go run ./cmd/internal/update_android_version

update_desktop_version:
	go run ./cmd/internal/update_desktop_version

build_android:
	cd ../sing-box-for-android && ./gradlew :app:clean :app:assembleOtherRelease :app:assembleOtherLegacyRelease && ./gradlew --stop

upload_android:
	mkdir -p dist/release_android
	cp ../sing-box-for-android/app/build/outputs/apk/other/release/*.apk dist/release_android
	cp ../sing-box-for-android/app/build/outputs/apk/otherLegacy/release/*.apk dist/release_android
	VERSION_CODE=$$(grep VERSION_CODE ../sing-box-for-android/version.properties | cut -d= -f2); \
	VERSION_NAME=$$(grep VERSION_NAME ../sing-box-for-android/version.properties | cut -d= -f2); \
	printf '{\n  "version_code": %s,\n  "version_name": "%s"\n}\n' "$$VERSION_CODE" "$$VERSION_NAME" > dist/release_android/SFA-version-metadata.json
	ghr --replace --draft --prerelease -p 5 "v${VERSION}" dist/release_android
	rm -rf dist/release_android

release_android: build_android upload_android

publish_android:
	cd ../sing-box-for-android && ./gradlew :app:publishPlayReleaseBundle && ./gradlew --stop

# TODO: find why and remove `-destination 'generic/platform=iOS'`
# TODO: remove xcode clean when fix control widget fixed
build_ios:
	cd ../sing-box-for-apple && \
	rm -rf build/SFI.xcarchive && \
	xcodebuild clean -scheme SFI -derivedDataPath build/SFI.dd && \
	xcodebuild archive -scheme SFI -configuration Release -destination 'generic/platform=iOS' -archivePath build/SFI.xcarchive -derivedDataPath build/SFI.dd -allowProvisioningUpdates | xcbeautify | grep -A 10 -e "Archive Succeeded" -e "ARCHIVE FAILED" -e "❌"

upload_ios_app_store:
	cd ../sing-box-for-apple && \
	xcodebuild -exportArchive -archivePath build/SFI.xcarchive -exportOptionsPlist SFI/Upload.plist -allowProvisioningUpdates

build_ios_deb:
	$(MAKE) -C ../sing-box-for-apple build_ios_deb

upload_ios_deb:
	ghr --replace --draft --prerelease "v${VERSION}" ../sing-box-for-apple/build/jailbreak/"SFI-${VERSION}-iphoneos-arm64.deb"

release_ios: build_ios upload_ios_app_store

release_ios_deb: build_ios_deb upload_ios_deb

build_macos:
	cd ../sing-box-for-apple && \
	rm -rf build/SFM.xcarchive && \
	xcodebuild archive -scheme SFM -configuration Release -archivePath build/SFM.xcarchive -derivedDataPath build/SFM.dd -allowProvisioningUpdates | xcbeautify | grep -A 10 -e "Archive Succeeded" -e "ARCHIVE FAILED" -e "❌"

upload_macos_app_store:
	cd ../sing-box-for-apple && \
	xcodebuild -exportArchive -archivePath build/SFM.xcarchive -exportOptionsPlist SFI/Upload.plist -allowProvisioningUpdates

release_macos: build_macos upload_macos_app_store

build_macos_standalone:
	$(MAKE) -C ../sing-box-for-apple archive_macos_standalone

build_macos_dmg:
	$(MAKE) -C ../sing-box-for-apple build_macos_dmg

build_macos_pkg:
	$(MAKE) -C ../sing-box-for-apple build_macos_pkg

notarize_macos_dmg:
	$(MAKE) -C ../sing-box-for-apple notarize_macos_dmg

notarize_macos_pkg:
	$(MAKE) -C ../sing-box-for-apple notarize_macos_pkg

upload_macos_dmg:
	mkdir -p dist/SFM
	cp ../sing-box-for-apple/build/SFM-Apple.dmg "dist/SFM/SFM-${VERSION}-Apple.dmg"
	cp ../sing-box-for-apple/build/SFM-Intel.dmg "dist/SFM/SFM-${VERSION}-Intel.dmg"
	cp ../sing-box-for-apple/build/SFM-Universal.dmg "dist/SFM/SFM-${VERSION}-Universal.dmg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Apple.dmg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Intel.dmg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Universal.dmg"

upload_macos_pkg:
	mkdir -p dist/SFM
	cp ../sing-box-for-apple/build/SFM-Apple.pkg "dist/SFM/SFM-${VERSION}-Apple.pkg"
	cp ../sing-box-for-apple/build/SFM-Intel.pkg "dist/SFM/SFM-${VERSION}-Intel.pkg"
	cp ../sing-box-for-apple/build/SFM-Universal.pkg "dist/SFM/SFM-${VERSION}-Universal.pkg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Apple.pkg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Intel.pkg"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}-Universal.pkg"

replace_macos_pkg:
	mkdir -p dist/SFM
	cp ../sing-box-for-apple/build/SFM-Apple.pkg "dist/SFM/SFM-${VERSION}-Apple.pkg"
	cp ../sing-box-for-apple/build/SFM-Intel.pkg "dist/SFM/SFM-${VERSION}-Intel.pkg"
	cp ../sing-box-for-apple/build/SFM-Universal.pkg "dist/SFM/SFM-${VERSION}-Universal.pkg"
	ghr --replace "v${VERSION}" "dist/SFM/SFM-${VERSION}-Apple.pkg"
	ghr --replace "v${VERSION}" "dist/SFM/SFM-${VERSION}-Intel.pkg"
	ghr --replace "v${VERSION}" "dist/SFM/SFM-${VERSION}-Universal.pkg"

upload_macos_dsyms:
	mkdir -p dist/SFM
	cd ../sing-box-for-apple/build/SFM.System-universal.xcarchive && zip -r SFM.dSYMs.zip dSYMs
	cp ../sing-box-for-apple/build/SFM.System-universal.xcarchive/SFM.dSYMs.zip "dist/SFM/SFM-${VERSION}.dSYMs.zip"
	ghr --replace --draft --prerelease "v${VERSION}" "dist/SFM/SFM-${VERSION}.dSYMs.zip"

replace_macos_dsyms:
	mkdir -p dist/SFM
	cd ../sing-box-for-apple/build/SFM.System-universal.xcarchive && zip -r SFM.dSYMs.zip dSYMs
	cp ../sing-box-for-apple/build/SFM.System-universal.xcarchive/SFM.dSYMs.zip "dist/SFM/SFM-${VERSION}.dSYMs.zip"
	ghr --replace "v${VERSION}" "dist/SFM/SFM-${VERSION}.dSYMs.zip"

release_macos_standalone: build_macos_pkg notarize_macos_pkg upload_macos_pkg upload_macos_dsyms

replace_macos_standalone: build_macos_pkg notarize_macos_pkg upload_macos_pkg upload_macos_dsyms

build_tvos:
	cd ../sing-box-for-apple && \
	rm -rf build/SFT.xcarchive && \
	xcodebuild archive -scheme SFT -configuration Release -archivePath build/SFT.xcarchive -derivedDataPath build/SFT.dd -allowProvisioningUpdates | xcbeautify | grep -A 10 -e "Archive Succeeded" -e "ARCHIVE FAILED" -e "❌"

upload_tvos_app_store:
	cd ../sing-box-for-apple && \
	xcodebuild -exportArchive -archivePath "build/SFT.xcarchive" -exportOptionsPlist SFI/Upload.plist -allowProvisioningUpdates

export_tvos_ipa:
	cd ../sing-box-for-apple && \
	xcodebuild -exportArchive -archivePath "build/SFT.xcarchive" -exportOptionsPlist SFI/Export.plist -allowProvisioningUpdates -exportPath build/SFT && \
	cp build/SFT/sing-box.ipa dist/SFT.ipa

upload_tvos_ipa:
	cd dist && \
	cp SFT.ipa "SFT-${VERSION}.ipa" && \
	ghr --replace --draft --prerelease "v${VERSION}" "SFT-${VERSION}.ipa"

release_tvos: build_tvos upload_tvos_app_store

update_apple_version:
	go run ./cmd/internal/update_apple_version

update_macos_version:
	MACOS_PROJECT_VERSION=$(shell go run -v ./cmd/internal/app_store_connect next_macos_project_version) go run ./cmd/internal/update_apple_version

release_apple: lib_apple update_apple_version release_ios release_macos release_tvos release_macos_standalone

release_apple_beta: update_apple_version release_ios release_macos release_tvos

publish_testflight:
	go run -v ./cmd/internal/app_store_connect publish_testflight $(filter-out $@,$(MAKECMDGOALS))

prepare_app_store:
	go run -v ./cmd/internal/app_store_connect prepare_app_store

publish_app_store:
	go run -v ./cmd/internal/app_store_connect publish_app_store

test:
	@go test -v ./... && \
	cd test && \
	go mod tidy && \
	go test -v -tags "$(TAGS_TEST)" .

test_stdio:
	@go test -v ./... && \
	cd test && \
	go mod tidy && \
	go test -v -tags "$(TAGS_TEST),force_stdio" .

lib_android:
	go run ./cmd/internal/build_libbox -target android

lib_apple:
	go run ./cmd/internal/build_libbox -target apple

lib_windows:
	$(SING_FFI) generate --config $(LIBBOX_FFI_CONFIG) --platform-type csharp

lib_android_new:
	$(SING_FFI) generate --config $(LIBBOX_FFI_CONFIG) --platform-type android

lib_apple_new:
	$(SING_FFI) generate --config $(LIBBOX_FFI_CONFIG) --platform-type apple

lib_install:
	go install -v github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
	go install -v github.com/sagernet/gomobile/cmd/gobind@v0.1.13

docs:
	venv/bin/mkdocs serve

publish_docs:
	venv/bin/mkdocs gh-deploy -m "Update" --force --ignore-version --no-history

docs_install:
	python3 -m venv venv
	source ./venv/bin/activate && pip install --force-reinstall mkdocs-material=="9.7.2" mkdocs-static-i18n=="1.2.3"

clean:
	rm -rf bin dist sidera sidera-low-memory
	rm -f $(shell go env GOPATH)/bin/sidera

update:
	git fetch
	git reset FETCH_HEAD --hard
	git clean -fdx

%:
	@:

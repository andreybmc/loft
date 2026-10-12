# 10.13 is the floor. Go cannot ship in-process (Go 1.20 was last for 10.13).
export MACOSX_DEPLOYMENT_TARGET := 10.13
MIN := 10.13
ARCH ?= $(shell uname -m)
ifeq ($(ARCH),x86_64)
TRIPLE := x86_64-apple-macosx$(MIN)
else
TRIPLE := arm64-apple-macosx$(MIN)
endif

SDK := $(shell xcrun --sdk macosx --show-sdk-path)
SWIFTC := xcrun swiftc
CLANG := xcrun clang
ROOT := $(abspath ../..)
# Intermediates only (.o, test_aead). The .app lives in dist/clients/macOS/.
OBJ := build
APP := $(ROOT)/dist/clients/macOS/loft.app
MACOS := $(APP)/Contents/MacOS
RES := $(APP)/Contents/Resources
FW := $(APP)/Contents/Frameworks
ROOTVER := $(shell cat $(ROOT)/VERSION 2>/dev/null || echo 0.1.0)
# Menu-bar marketing version. Patch +1 on every `make app` (0.1.0 → 0.1.1 → 0.1.2).
# CI: BUMP=0 stamps Info.plist from the file and leaves VERSION alone.
MACVER_FILE := VERSION
BUMP ?= 1

SWIFT_SRC := $(wildcard Sources/Engine/*.swift Sources/App/*.swift)
C_OBJ := $(OBJ)/chacha.o $(OBJ)/utun.o $(OBJ)/stack.o

.PHONY: all app dmg run clean test-crypto shot front-probe

all: app

# Each `make app` / `make macos` raises the patch, then stamps Info.plist.
# run / shot / front-probe do not bump. CI: `make dmg BUMP=0`.
# Sign: ad-hoc unless CODESIGN_IDENTITY is a Developer ID (see pack/identity.sh).
app: test-crypto $(APP)/Contents/MacOS/loft
	@if [ "$(BUMP)" = "0" ]; then \
		v=$$(tr -d ' \n' < $(MACVER_FILE)); \
	else \
		v=$$(awk -F. '{printf "%d.%d.%d", $$1, $$2, $$3+1}' $(MACVER_FILE)); \
		echo $$v > $(MACVER_FILE); \
	fi; \
	sed "s/__LOFT_VERSION__/$$v/g" Info.plist > $(APP)/Contents/Info.plist; \
	sh pack/sign.sh $(APP); \
	echo "built $(APP)  $$v  (macOS $(MIN)+)"

# Disk image: loft.app + Applications symlink + README.txt.
# CODESIGN_IDENTITY / NOTARY_PROFILE as for `app`.
dmg: app
	sh pack/make-dmg.sh

test-crypto: $(OBJ)/chacha.o
	mkdir -p $(OBJ)
	$(CLANG) tests/test_aead.c $(OBJ)/chacha.o -o $(OBJ)/test_aead \
		-mmacosx-version-min=$(MIN) -O2 -isysroot $(SDK) -I Sources/Crypto
	./$(OBJ)/test_aead

$(OBJ)/chacha.o: Sources/Crypto/chacha20poly1305.c Sources/Crypto/chacha20poly1305.h
	mkdir -p $(OBJ)
	$(CLANG) -c Sources/Crypto/chacha20poly1305.c -o $(OBJ)/chacha.o -mmacosx-version-min=$(MIN) -O2 -isysroot $(SDK)

$(OBJ)/utun.o: Sources/TUN/utun.c Sources/TUN/utun.h
	mkdir -p $(OBJ)
	$(CLANG) -c Sources/TUN/utun.c -o $(OBJ)/utun.o -mmacosx-version-min=$(MIN) -O2 -isysroot $(SDK)

$(OBJ)/stack.o: Sources/TUN/stack.c Sources/TUN/stack.h
	mkdir -p $(OBJ)
	$(CLANG) -c Sources/TUN/stack.c -o $(OBJ)/stack.o -mmacosx-version-min=$(MIN) -O2 -isysroot $(SDK)

$(MACOS)/loft-priv: Sources/TUN/loft-priv.c $(OBJ)/utun.o
	mkdir -p $(MACOS)
	$(CLANG) Sources/TUN/loft-priv.c $(OBJ)/utun.o -o $(MACOS)/loft-priv \
		-mmacosx-version-min=$(MIN) -O2 -isysroot $(SDK) -I Sources/TUN

FRONT_GO := $(shell find $(ROOT)/clients/go $(ROOT)/internal -name '*.go' 2>/dev/null)

$(MACOS)/loft-front: $(ROOT)/go.mod $(FRONT_GO)
	mkdir -p $(MACOS)
	cd $(ROOT) && go build -trimpath -ldflags="-s -w -X loft/internal/version.Version=$(ROOTVER)" \
		-o dist/clients/macOS/loft.app/Contents/MacOS/loft-front ./clients/go/cmd/loft-front

$(APP)/Contents/MacOS/loft: $(SWIFT_SRC) $(C_OBJ) $(MACOS)/loft-priv $(MACOS)/loft-front
	mkdir -p $(MACOS) $(RES) $(FW)
	$(SWIFTC) -swift-version 5 -target $(TRIPLE) -sdk $(SDK) -O \
		-import-objc-header Sources/Bridging-Header.h \
		-I Sources \
		-framework AppKit -framework Foundation -framework Security -framework QuartzCore \
		-Xlinker $(OBJ)/chacha.o -Xlinker $(OBJ)/utun.o -Xlinker $(OBJ)/stack.o \
		-Xlinker -rpath -Xlinker @executable_path/../Frameworks \
		$(SWIFT_SRC) \
		-o $(MACOS)/loft
	install_name_tool -add_rpath "@executable_path/../Frameworks" $(MACOS)/loft 2>/dev/null || true
	printf 'APPL????' > $(APP)/Contents/PkgInfo
	cp Resources/loft.icns $(RES)/
	cp Resources/loftTemplate*.png $(RES)/
	xcrun swift-stdlib-tool --copy --scan-executable $(MACOS)/loft \
	  --destination $(FW) --platform macosx --unsigned-destination $(FW)
	rm -rf $(FW)/macosx
	# Nested sign happens in `app` via pack/sign.sh (identity or ad-hoc).

run: $(APP)/Contents/MacOS/loft
	open $(APP)

# Debug: PNG dump of windows → /tmp/loft-shots (needs AppKit; flag lives in main.swift).
shot: $(APP)/Contents/MacOS/loft
	$(MACOS)/loft --shot

# Debug: one loft-front handshake, no TUN (same binary as the app).
front-probe: $(APP)/Contents/MacOS/loft
	$(MACOS)/loft --probe

clean:
	rm -rf $(OBJ) $(APP)

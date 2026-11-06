.PHONY: setup dev build build-all test clean

setup:
	node scripts/setup-toolchain.mjs --install-fe

dev:
	node scripts/dev.mjs

build:
	node scripts/build.mjs

build-all:
	node scripts/build.mjs --all

test:
	node scripts/test.mjs

clean:
	rm -rf dist frontend/dist backend/webdist/dist/*

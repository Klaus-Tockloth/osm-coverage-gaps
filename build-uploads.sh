#!/bin/sh

# ------------------------------------
# Purpose:
# - Builds uploads (tar.gz or zip) for Github project repository (assets in release section).
#
# Releases:
# - v1.0.0 - 2026-11-20: initial release
# ------------------------------------

# set -o xtrace
set -o verbose

# recreate directory
rm -r ./uploads
mkdir ./uploads

# uploads 'darwin'
tar -cvzf ./uploads/macos-amd64_gic.tar.gz ./binaries/darwin-amd64/osm-coverage-gaps
tar -cvzf ./uploads/macos-arm64_gic.tar.gz ./binaries/darwin-arm64/osm-coverage-gaps

# uploads 'linux'
tar -cvzf ./uploads/linux-amd64_gic.tar.gz ./binaries/linux-amd64/osm-coverage-gaps
tar -cvzf ./uploads/linux-arm64_gic.tar.gz ./binaries/linux-arm64/osm-coverage-gaps

# uploads 'windows'
zip ./uploads/windows-amd64_gic.zip ./binaries/windows-amd64/osm-coverage-gaps.exe
zip ./uploads/windows-arm64_gic.zip ./binaries/windows-arm64/osm-coverage-gaps.exe

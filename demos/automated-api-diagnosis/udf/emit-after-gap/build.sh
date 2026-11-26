#!/bin/sh
# Tests the function and builds the jar Terraform uploads, to tf/udf/emit-after-gap.jar.
#
# It builds in a container so the jar does not depend on the Java and Maven on the machine:
# the same image gives the same bytes here and in CI, and CI relies on that to check that
# the committed jar is the one this source builds. Only Docker is needed.
#
#   ./build.sh
set -eu

# Pinned by tag. Change it and rebuild the jar in the same commit.
IMAGE=maven:3.9.9-eclipse-temurin-17

here=$(cd "$(dirname "$0")" && pwd)
dest="$here/../../tf/udf"
mkdir -p "$dest"

docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e MAVEN_CONFIG=/tmp/.m2 \
  -v "$here":/project \
  -w /project \
  "$IMAGE" \
  mvn --batch-mode --no-transfer-progress -Dmaven.repo.local=/tmp/.m2/repository clean package

cp "$here/target/emit-after-gap.jar" "$dest/emit-after-gap.jar"
echo "Wrote $dest/emit-after-gap.jar"

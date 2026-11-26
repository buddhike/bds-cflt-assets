#!/bin/sh
# Tests the function and builds the jar, target/emit-after-gap.jar.
#
# It builds in a container so the jar does not depend on the Java and Maven on the machine.
# Only Docker is needed. CI runs this script and publishes the jar as a release, which is
# where Terraform gets it: the jar is not committed. Run it yourself to test a change.
#
#   ./build.sh
set -eu

# Pinned by tag.
IMAGE=maven:3.9.9-eclipse-temurin-17

here=$(cd "$(dirname "$0")" && pwd)

docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e MAVEN_CONFIG=/tmp/.m2 \
  -v "$here":/project \
  -w /project \
  "$IMAGE" \
  mvn --batch-mode --no-transfer-progress -Dmaven.repo.local=/tmp/.m2/repository clean package

echo "Built $here/target/emit-after-gap.jar"

#!/bin/bash

ROOT_DIR=$(git rev-parse --show-toplevel)
CSV="$ROOT_DIR/bundle/manifests/file-integrity-operator.clusterserviceversion.yaml"

OLD_VERSION=$(yq -r '.spec.version' "$CSV")
echo $OLD_VERSION

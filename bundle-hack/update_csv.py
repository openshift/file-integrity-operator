#!/usr/bin/env python3
# The syntax str | None was added in 3.10
# Remove when UBI9 moves to python 3.10+
from __future__ import annotations

import argparse
import base64
import io
import json
import os
import sys
import shutil

from ruamel.yaml import YAML


yaml = YAML()
yaml.preserve_quotes = True


parser = argparse.ArgumentParser()
parser.add_argument("manifest_directory", type=str,
                    help="Path to operator manifest directory.")
parser.add_argument("version", type=str,
                    help="Version string of the product to update in the manifest.")
args = parser.parse_args()


def main() -> None:
    build_manifest_file_path = get_manifest_file_path_from_directory(args.manifest_directory)
    if not build_manifest_file_path:
        print(f"Failed to find manifest in {args.manifest_directory}")
        sys.exit(1)

    print(f"Success to find manifest in {build_manifest_file_path}")
    with open(build_manifest_file_path) as f:
        manifest = yaml.load(f)
        print(f"Successfully loaded {build_manifest_file_path} from build.")

    add_required_annotations(manifest)
    replace_version(manifest)
    replace_icon(manifest)
    replace_images(manifest)
    remove_related_images(manifest)
    write_manifest(manifest)
    print(f"Successfully updated CSV manifest for {args.version}.")



def get_manifest_file_path_from_directory(d: str) -> str|None:
    for filename in os.listdir(d):
        if filename.endswith('clusterserviceversion.yaml'):
            return os.path.join(d, filename)

def add_required_annotations(m: dict) -> None:
    """
    Adds required annotations to the CSV file content represented as a dictionary.
    Errors out if 'metadata' or 'annotations' does not exist.

    :param m: A dictionary representing the operator CSV manifest.
    """
    required_annotations = {
        "features.operators.openshift.io/disconnected": "true",
        "features.operators.openshift.io/fips-compliant": "true",
        "features.operators.openshift.io/proxy-aware": "false",
        "features.operators.openshift.io/tls-profiles": "false",
        "features.operators.openshift.io/token-auth-aws": "false",
        "features.operators.openshift.io/token-auth-azure": "false",
        "features.operators.openshift.io/token-auth-gcp": "false"
    }

    if 'metadata' not in m:
        sys.exit("Error: 'metadata' does not exist in the CSV content.")

    if 'annotations' not in m['metadata']:
        sys.exit("Error: 'annotations' does not exist within 'metadata' in the CSV content.")

    for key, value in required_annotations.items():
        m['metadata']['annotations'][key] = value

    print("Successfully added required annotations.")

def replace_version(m: dict) -> None:
    manifest_version = m['spec']['version']
    print(f"Updating version references from {manifest_version}",
          f"to {args.version} in manifest.")
    m['metadata']['name'] = m['metadata']['name'].replace(manifest_version, args.version)
    m['metadata']['annotations']['olm.skipRange'] = m['metadata']['annotations']['olm.skipRange'].replace(manifest_version, args.version)
    m['spec']['replaces'] = 'file-integrity-operator.v' + manifest_version
    m['spec']['version'] = args.version
    print(f"Successfully updated the operator version references from",
          f"{manifest_version} to {args.version} in manifest.")


def replace_icon(m: dict) -> None:
    """Replace the upstream icon with a Red Hat branded image.

    Perform an in-place update of the icon data on the manifest.

    manifest(dict): A dictionary representing the operator CSV manifest
    """
    icon_path = "icons/icon.png"
    with io.open(icon_path, "rb") as f:
        base64_encoded_icon = base64.b64encode(f.read())
    icons = [{"base64data": base64_encoded_icon.decode(), "mediatype": "image/png"}]
    m['spec']['icon'] = icons
    print(f"Successfully updated the operator image to use icon in {icon_path}.")


def replace_images(m: dict) -> None:
    # Get operator build image, which is available through the environment
    # variable as a JSON string. This means we don't have to query brew
    # directly for data about builds.

    FIO_IMAGE_PULLSPEC = "quay.io/redhat-user-workloads/ocp-isc-tenant/file-integrity-operator@sha256:ffd07e3abe5b0c3f863d072a77a81fced02df355e906bca71d2cdc2f8f907850"


    # This is incredibly specific to how the File Integrity Operator CSV is
    # written. If something changes upstream, it could break this format,
    # likely resulting in a KeyError (if the dictionary we're looking for
    # doesn't exist), IndexError (if the number of items in the list doesn't
    # match our expectations), or TypeError (if we're trying to fish a key out
    # of a list).
    m['spec']['install']['spec']['deployments'][0]['spec']['template']['spec']['containers'][0]['image'] = FIO_IMAGE_PULLSPEC
    container_env = m['spec']['install']['spec']['deployments'][0]['spec']['template']['spec']['containers'][0]['env']
    for e in container_env:
        if e['name'] == "RELATED_IMAGE_OPERATOR":
            e['value'] = FIO_IMAGE_PULLSPEC
    print("Successfully updated the operator image to use downstream builds.")


def remove_related_images(m: dict) -> None:
    # Remove relatedImages entirely from the CSV. OSBS will look for container
    # images in the manifest and populate them as relatedImages when the bundle
    # image is built, so that we don't have to. See
    # https://osbs.readthedocs.io/en/latest/users.html#pullspec-locations for
    # more information on how OSBS does this.
    del m['spec']['relatedImages']
    print("Removed relatedImages from operator manifest.")


def write_manifest(m: dict) -> None:
    old_csv_filename = get_manifest_file_path_from_directory('manifests')
    if not old_csv_filename:
        print(f"Failed to find manifest in {args.manifest_directory}")
        sys.exit(1)

    new_csv_filename = os.path.join('manifests', f"file-integrity-operator.v{args.version}.clusterserviceversion.yaml")
    os.rename(old_csv_filename, new_csv_filename)
    print(f"Successfully moved {old_csv_filename} to {new_csv_filename}.")
    with open(new_csv_filename, 'w') as f:
        yaml.dump(m, f)
        print(f"Successfully wrote updated manifest to {new_csv_filename}.")


if __name__ == "__main__":
    main()

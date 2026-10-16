"""Download the official Ubuntu 18.04 amd64 build image through host curl.

Creates a digest-checked Docker load archive when the local WSL daemon cannot
reach Docker Hub. Registry tokens are kept in memory and never logged.
"""

import argparse
import gzip
import hashlib
import io
import json
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path


REGISTRY = "https://registry-1.docker.io/v2/library/ubuntu"
ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])


def fetch(url, output, token=None, accept=None):
    args = ["curl.exe" if shutil.which("curl.exe") else "curl", "-fsSL",
            "--connect-timeout", "15", "--max-time", "600",
            "--retry", "3", "--retry-delay", "2", "--output", str(output)]
    if token:
        args.extend(["-H", "Authorization: Bearer " + token])
    if accept:
        args.extend(["-H", "Accept: " + accept])
    subprocess.run(args + [url], check=True)


def digest(path):
    with path.open("rb") as stream:
        return "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest()


def checked_blob(descriptor, output, token):
    fetch(REGISTRY + "/blobs/" + descriptor["digest"], output, token)
    if digest(output) != descriptor["digest"]:
        raise ValueError("Registry blob digest mismatch")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.output.exists():
        raise FileExistsError(args.output)
    with tempfile.TemporaryDirectory(prefix="ubuntu-image-") as folder:
        staging = Path(folder)
        auth = staging / "auth.json"
        fetch("https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/ubuntu:pull", auth)
        token = json.loads(auth.read_text())["token"]
        auth.unlink()
        manifest_path = staging / "registry-manifest.json"
        fetch(REGISTRY + "/manifests/18.04", manifest_path, token, ACCEPT)
        manifest = json.loads(manifest_path.read_text())
        if "manifests" in manifest:
            descriptor = next(item for item in manifest["manifests"]
                              if item.get("platform", {}).get("os") == "linux"
                              and item["platform"].get("architecture") == "amd64")
            fetch(REGISTRY + "/manifests/" + descriptor["digest"], manifest_path, token, ACCEPT)
            if digest(manifest_path) != descriptor["digest"]:
                raise ValueError("Registry manifest digest mismatch")
            manifest = json.loads(manifest_path.read_text())
        config_path = staging / (manifest["config"]["digest"].split(":")[1] + ".json")
        checked_blob(manifest["config"], config_path, token)
        diff_ids = json.loads(config_path.read_text())["rootfs"]["diff_ids"]
        layer_names = []
        print("Official Ubuntu 18.04 amd64 config: " + manifest["config"]["digest"], flush=True)
        for index, descriptor in enumerate(manifest["layers"]):
            print("Downloading layer " + str(index + 1), flush=True)
            compressed = staging / "layer.gz"
            checked_blob(descriptor, compressed, token)
            relative = descriptor["digest"].split(":")[1] + "/layer.tar"
            layer_path = staging / relative
            layer_path.parent.mkdir()
            with gzip.open(compressed, "rb") as source, layer_path.open("wb") as target:
                shutil.copyfileobj(source, target)
            if digest(layer_path) != diff_ids[index]:
                raise ValueError("Uncompressed layer digest mismatch")
            compressed.unlink()
            layer_names.append(relative)
        docker_manifest = json.dumps([{"Config": config_path.name,
                                      "RepoTags": ["ubuntu:18.04"], "Layers": layer_names}]).encode()
        with tarfile.open(args.output, "w") as archive:
            archive.add(config_path, arcname=config_path.name)
            for relative in layer_names:
                archive.add(staging / relative, arcname=relative)
            info = tarfile.TarInfo("manifest.json")
            info.size = len(docker_manifest)
            archive.addfile(info, io.BytesIO(docker_manifest))
        print("Docker archive SHA256: " + digest(args.output), flush=True)


if __name__ == "__main__":
    main()

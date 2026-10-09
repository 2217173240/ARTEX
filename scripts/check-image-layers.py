#!/usr/bin/env python3
"""Scan every saved Docker layer for synthetic canaries without extracting files."""
import argparse
import io
import json
import pathlib
import tarfile


def contains_canary(stream, canaries, chunk_size=1024 * 1024):
    overlap = max(map(len, canaries)) - 1
    tail = b""
    while True:
        chunk = stream.read(chunk_size)
        if not chunk:
            return False
        data = tail + chunk
        if any(canary in data for canary in canaries):
            return True
        tail = data[-overlap:] if overlap else b""


def check_image(stream, canaries):
    checked = 0
    contaminated = False
    with tarfile.open(fileobj=stream, mode="r:*") as image:
        manifest_file = image.extractfile("manifest.json")
        if manifest_file is None:
            raise ValueError("Docker save manifest is missing")
        manifest = json.load(manifest_file)
        layers = list(dict.fromkeys(layer for entry in manifest for layer in entry["Layers"]))
        if not layers:
            raise ValueError("Docker save contains no layers")
        for layer in layers:
            layer_stream = image.extractfile(layer)
            if layer_stream is None:
                raise ValueError("Docker save layer is missing")
            with tarfile.open(fileobj=layer_stream, mode="r|*") as contents:
                for member in contents:
                    metadata = (member.name + "\0" + member.linkname).encode()
                    leaked = any(canary in metadata for canary in canaries)
                    if member.isfile():
                        member_stream = contents.extractfile(member)
                        if member_stream is None:
                            raise ValueError("Layer file could not be read")
                        leaked = contains_canary(member_stream, canaries) or leaked
                    contaminated = contaminated or leaked
            checked += 1
    return checked, contaminated


def saved_image(payloads):
    result = io.BytesIO()
    with tarfile.open(fileobj=result, mode="w") as image:
        layers = []
        for index, payload in enumerate(payloads):
            layer = io.BytesIO()
            with tarfile.open(fileobj=layer, mode="w") as archive:
                member = tarfile.TarInfo("../../unsafe-name")
                member.size = len(payload)
                archive.addfile(member, io.BytesIO(payload))
            name = f"{index}/layer.tar"
            layers.append(name)
            data = layer.getvalue()
            member = tarfile.TarInfo(name)
            member.size = len(data)
            image.addfile(member, io.BytesIO(data))
        data = json.dumps([{"Layers": layers}]).encode()
        member = tarfile.TarInfo("manifest.json")
        member.size = len(data)
        image.addfile(member, io.BytesIO(data))
    result.seek(0)
    return result


def self_test():
    canary = b"synthetic-layer-canary"
    assert contains_canary(io.BytesIO(b"abc" + canary + b"end"), [canary], 4)
    assert not contains_canary(io.BytesIO(b"safe bytes"), [canary], 4)
    assert check_image(saved_image([b"safe", b"safe"]), [canary]) == (2, False)
    # An earlier contaminated layer must fail even when the final layer is clean.
    assert check_image(saved_image([canary, b"safe"]), [canary]) == (2, True)
    print("Layer scanner self-tests passed.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image", nargs="?", type=pathlib.Path)
    parser.add_argument("--canaries", type=pathlib.Path)
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return 0
    if args.image is None or args.canaries is None:
        parser.error("image and --canaries are required")
    canaries = [value.encode() for value in json.loads(args.canaries.read_text())]
    if not canaries or any(not value for value in canaries):
        parser.error("canaries must contain nonempty strings")
    with args.image.open("rb") as image:
        count, contaminated = check_image(image, canaries)
    print(f"Scanned {count} image layers; synthetic canary detected: {contaminated}.")
    return int(contaminated)


if __name__ == "__main__":
    raise SystemExit(main())

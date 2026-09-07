#!/usr/bin/env python3
"""Upload the Blender reference video to a private TOS bucket and print a signed URL."""

from __future__ import annotations

import argparse
import getpass
import json
import os
from pathlib import Path

import tos


def credentials() -> tuple[str, str]:
    return getpass.getpass("AccessKeyID: ").strip(), getpass.getpass("SecretAccessKey: ").strip()


def client(ak: str, sk: str, region: str) -> tos.TosClientV2:
    return tos.TosClientV2(ak, sk, f"tos-{region}.volces.com", region)


def list_buckets(ak: str, sk: str) -> int:
    output = client(ak, sk, "cn-beijing").list_buckets()
    buckets = [
        {
            "name": bucket.name,
            "region": getattr(bucket, "location", None),
            "extranet_endpoint": getattr(bucket, "extranet_endpoint", None),
        }
        for bucket in output.buckets
    ]
    print(json.dumps(buckets, ensure_ascii=False, indent=2))
    return 0


def upload(
    ak: str,
    sk: str,
    bucket: str,
    region: str,
    source: Path,
    key: str,
    url_file: Path | None,
) -> int:
    tos_client = client(ak, sk, region)
    tos_client.put_object_from_file(
        bucket,
        key,
        str(source),
        content_type="video/mp4",
    )
    signed = tos_client.pre_signed_url(tos.HttpMethodType.Http_Method_Get, bucket, key, expires=86400)
    result = {"bucket": bucket, "region": region, "key": key}
    if url_file is None:
        result["url"] = signed.signed_url
    else:
        url_file.write_text(f"{signed.signed_url}\n", encoding="utf-8")
        os.chmod(url_file, 0o600)
        result["url_file"] = str(url_file.resolve())
    print(json.dumps(result, ensure_ascii=False))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--bucket")
    parser.add_argument("--region")
    parser.add_argument("--file", type=Path)
    parser.add_argument("--key", default="seedance-demo/shenzhou5-onboard-close-orbit.mp4")
    parser.add_argument("--url-file", type=Path)
    args = parser.parse_args()
    ak, sk = credentials()
    if args.list:
        return list_buckets(ak, sk)
    if not (args.bucket and args.region and args.file):
        parser.error("upload requires --bucket, --region, and --file")
    if not args.file.is_file():
        parser.error(f"file not found: {args.file}")
    return upload(ak, sk, args.bucket, args.region, args.file, args.key, args.url_file)


if __name__ == "__main__":
    raise SystemExit(main())

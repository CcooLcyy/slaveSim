#!/usr/bin/env python3
"""生成双架构 slaveSim 发布包，并将校验后的版本发布到 R2。"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

from versioning import artifact_version, image_name, validate_commit, version_order

ARCHES = ("amd64", "arm64")
SOURCE_DIR = Path(__file__).resolve().parents[1]
PACKAGE_FILES = ("config.example.json", "README.md", "VERSION", "scripts/update.py", "scripts/versioning.py", "scripts/runtime.py",
                 "deploy/updater.runtime.example.json",
                 "doc/单容器网页更新.md",
                 "deploy/compose.release.yml", "deploy/slavesim-update.service",
                 "deploy/slavesim-update.timer")
# 预设点表随发布包提供，部署时由用户选择，不替换已有运行配置。
INVERTER_FILES = (
    "config.inverters.example.json",
    "configs/inverters/README.md",
    "configs/inverters/aotai-growatt.md",
    "configs/inverters/goodwe.md",
    "configs/inverters/huawei.md",
    "configs/inverters/aotai-hf-tl-telemetry.json",
    "configs/inverters/aotai-tld-telemetry.json",
    "configs/inverters/aotai-tlc10-30-telemetry.json",
    "configs/inverters/aotai-asp250-telemetry.json",
    "configs/inverters/aotai-asp500-telemetry.json",
    "configs/inverters/aotai-asp630-telemetry.json",
    "configs/inverters/aotai-controls-common.json",
    "configs/inverters/aotai-controls-large.json",
    "configs/inverters/growatt-tl3x-mid-mac.json",
    "configs/inverters/growatt-tl3x-max.json",
    "configs/inverters/growatt-tlx-min.json",
    "configs/inverters/goodwe-mtg2.json",
    "configs/inverters/goodwe-smt50-60k.json",
    "configs/inverters/goodwe-sdtg2.json",
    "configs/inverters/goodwe-single-mppt.json",
    "configs/inverters/goodwe-single-dual-mppt.json",
    "configs/inverters/huawei-v3.json",
)
PACKAGE_FILES += INVERTER_FILES
HEX64 = re.compile(r"^[0-9a-f]{64}$")


class RejectRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        raise ValueError("公开发布地址不允许重定向")


def urlopen(request, timeout=30):
    # R2 公共对象无需跳转，避免校验请求被重定向到其他来源或明文连接。
    # 显式标识发布工具，避免 Cloudflare 拒绝 urllib 的默认请求标识。
    request.add_header("User-Agent", "slavesim-publisher/1")
    return build_opener(RejectRedirect()).open(request, timeout=timeout)


def public_url(value: str) -> str:
    """公开根地址仅接受不包含凭据、参数或片段的 HTTPS 地址。"""
    parsed = urlsplit(value)
    try:
        parsed.port
    except ValueError as exc:
        raise ValueError("公开根地址端口不合法") from exc
    if (parsed.scheme != "https" or not parsed.netloc or not parsed.hostname
            or parsed.username is not None or parsed.password is not None
            or parsed.query or parsed.fragment or parsed.path.strip("/")
            or any(char.isspace() or ord(char) < 32 for char in value)
            or "\\" in value):
        raise ValueError("公开根地址必须是无凭据、路径和参数的 HTTPS 地址")
    return value.rstrip("/")


def valid_prefix(value: str) -> str:
    parts = value.split("/")
    if not value or any(part in ("", ".", "..") or not re.fullmatch(r"[A-Za-z0-9._-]+", part) for part in parts):
        raise ValueError("发布前缀只能包含安全的相对路径，不允许空路径、点或上级目录")
    return value


def metadata(version: str, channel: str, commit: str, prefix: str, base_url: str) -> tuple[str, str]:
    if channel not in ("ci", "stable"):
        raise ValueError("发布通道只能是 ci 或 stable")
    validate_commit(channel, version, commit)
    return valid_prefix(prefix), public_url(base_url)


def timestamp(value: str | None) -> str:
    value = value or datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            raise ValueError("时间缺少时区")
    except ValueError as exc:
        raise ValueError("发布时间必须是包含时区的 ISO8601 时间") from exc
    return parsed.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def encoded_url(base: str, key: str) -> str:
    return base + "/" + "/".join(quote(part, safe="") for part in key.split("/"))


def json_bytes(value: dict) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")


def same_release(first: dict, second: dict) -> bool:
    # 发布时间保留首次发布的值，其余全部发布元数据必须一致。
    return {key: value for key, value in first.items() if key != "published_at"} == {
        key: value for key, value in second.items() if key != "published_at"}


def write_immutable(path: Path, data: bytes) -> None:
    if path.exists():
        if not path.is_file() or path.read_bytes() != data:
            raise ValueError(f"拒绝覆盖已有发布文件: {path}")
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def binary_package(destination: Path, files: list[tuple[str, Path, int]]) -> None:
    # 固定 gzip 和 tar 的时间、用户、权限，保证同一输入可重现。
    with destination.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            for name, source, mode in files:
                content = source.read_bytes()
                info = tarfile.TarInfo(name)
                info.mode = mode
                info.size = len(content)
                info.mtime = 0
                archive.addfile(info, io.BytesIO(content))


def prepare(*, input_dir: Path, output_dir: Path, version: str, channel: str,
            commit: str, public_base_url: str, prefix: str = "slavesim",
            published_at: str | None = None, source_dir: Path = SOURCE_DIR) -> dict:
    prefix, base = metadata(version, channel, commit, prefix, public_base_url)
    when = timestamp(published_at)
    input_dir, output_dir, source_dir = Path(input_dir), Path(output_dir), Path(source_dir)
    for arch in ARCHES:
        for name in ("slavesim", "probe", "slavesim-image.tar.gz", "image-id.txt"):
            if not (input_dir / arch / name).is_file():
                raise ValueError(f"缺少 {arch} 发布输入: {name}")
        image_id = (input_dir / arch / "image-id.txt").read_text(encoding="utf-8").strip()
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
            raise ValueError(f"{arch} image_id 格式不合法")
    for name in PACKAGE_FILES:
        if not (source_dir / name).is_file():
            raise ValueError(f"缺少发布包说明或部署工具: {name}")
    existing_file = output_dir / "release.json"
    existing = json.loads(existing_file.read_text(encoding="utf-8")) if existing_file.exists() else None
    if existing and (existing.get("version") != version or existing.get("commit") != commit
                     or existing.get("channel") != channel):
        raise ValueError("拒绝覆盖输出目录中的另一版本或提交")
    if existing:
        when = existing["published_at"]
    manifest = {"schema_version": 1, "project": "slavesim", "channel": channel,
                "version": version, "commit": commit, "published_at": when, "platforms": {}}
    asset_version = artifact_version(version)
    checksum_lines = []
    with tempfile.TemporaryDirectory(prefix="slavesim-release-") as temporary:
        staging = Path(temporary)
        for arch in ARCHES:
            directory = staging / "assets" / asset_version / arch
            directory.mkdir(parents=True)
            image_file_name = f"slavesim-{asset_version}-linux-{arch}-image.tar.gz"
            binary_name = f"slavesim-{asset_version}-linux-{arch}.tar.gz"
            shutil.copyfile(input_dir / arch / "slavesim-image.tar.gz", directory / image_file_name)
            files = [(name, input_dir / arch / name, 0o755) for name in ("slavesim", "probe")]
            files += [(name, source_dir / name, 0o755 if name.endswith(".py") else 0o644) for name in PACKAGE_FILES]
            binary_package(directory / binary_name, files)
            image_path, binary_path = directory / image_file_name, directory / binary_name
            image_sha, binary_sha = digest(image_path), digest(binary_path)
            key = f"{prefix}/{channel}/{asset_version}/{arch}"
            manifest["platforms"]["linux-" + arch] = {
                "url": encoded_url(base, key + "/" + image_file_name), "sha256": image_sha,
                "size": image_path.stat().st_size, "image": image_name(version, arch),
                "binary_url": encoded_url(base, key + "/" + binary_name), "binary_sha256": binary_sha,
                "binary_size": binary_path.stat().st_size,
                "image_id": (input_dir / arch / "image-id.txt").read_text(encoding="utf-8").strip()}
            checksum_lines += [f"{image_sha}  {arch}/{image_file_name}", f"{binary_sha}  {arch}/{binary_name}"]
        if existing and not same_release(existing, manifest):
            raise ValueError("拒绝覆盖相同版本下内容不同的发布包")
        # 全部输入、归档和清单通过检查之后才写入最终产物。
        for artifact in sorted((staging / "assets").rglob("*.tar.gz")):
            write_immutable(output_dir / artifact.relative_to(staging), artifact.read_bytes())
        write_immutable(output_dir / "SHA256SUMS", ("\n".join(checksum_lines) + "\n").encode())
        write_immutable(output_dir / "release.json", json_bytes(manifest))
        write_immutable(output_dir / "latest.json", json_bytes(manifest))
    print(f"已生成 {channel} {version} 双架构发布包: {output_dir}")
    return manifest


def get_json(url: str, missing_ok: bool = False) -> dict | None:
    try:
        with urlopen(Request(url, headers={"Cache-Control": "no-cache"}), timeout=30) as response:
            raw = response.read(1024 * 1024 + 1)
            if len(raw) > 1024 * 1024:
                raise ValueError("公开更新清单超过大小限制")
            value = json.loads(raw)
            if not isinstance(value, dict):
                raise ValueError("公开更新清单必须是 JSON 对象")
            return value
    except HTTPError as exc:
        if missing_ok and exc.code == 404:
            return None
        raise


def verify_asset(url: str, expected_sha: str, expected_size: int) -> None:
    hasher = hashlib.sha256()
    count = 0
    with urlopen(Request(url, headers={"Cache-Control": "no-cache"}), timeout=30) as response:
        while chunk := response.read(1024 * 1024):
            count += len(chunk)
            if count > expected_size:
                raise ValueError(f"公开资产长度校验失败: {url}")
            hasher.update(chunk)
    if count != expected_size or hasher.hexdigest() != expected_sha:
        raise ValueError(f"公开资产 SHA256 或长度校验失败: {url}")
    print(f"公开资产校验通过: {url}")


def upload(source: Path, key: str, bucket: str, endpoint: str, content_type: str, cache_control: str) -> None:
    print(f"上传 R2 对象: {key}")
    subprocess.run(["aws", "s3", "cp", str(source), f"s3://{bucket}/{key}",
                    "--endpoint-url", endpoint, "--region", "auto", "--content-type", content_type,
                    "--cache-control", cache_control, "--only-show-errors"], check=True)


def list_channel_objects(bucket: str, endpoint: str, channel_root: str) -> set[str]:
    """完整读取并校验所有分页；列举失败时不能产生部分删除计划。"""
    keys = set()
    tokens = set()
    token = None
    while True:
        args = ["aws", "s3api", "list-objects-v2", "--bucket", bucket, "--prefix", channel_root,
                "--endpoint-url", endpoint, "--region", "auto", "--output", "json", "--no-paginate"]
        if token is not None:
            args += ["--continuation-token", token]
        result = subprocess.run(args, check=True, capture_output=True, text=True, encoding="utf-8")
        page = json.loads(result.stdout)
        if (not isinstance(page, dict) or type(page.get("IsTruncated")) is not bool
                or not isinstance(page.get("Contents", []), list)):
            raise ValueError("R2 对象列表格式不合法，拒绝清理")
        for entry in page.get("Contents", []):
            key = entry.get("Key") if isinstance(entry, dict) else None
            if not isinstance(key, str) or not key.startswith(channel_root):
                raise ValueError("R2 对象列表越出当前通道，拒绝清理")
            keys.add(key)
        if not page["IsTruncated"]:
            return keys
        token = page.get("NextContinuationToken")
        if not isinstance(token, str) or not token or token in tokens:
            raise ValueError("R2 对象列表分页异常，拒绝清理")
        tokens.add(token)


def cleanup_old_versions(*, bucket: str, endpoint: str, channel_root: str, keep_keys: set[str],
                         manifest: dict, latest_url: str) -> int:
    # 仅使用发布器已验证的精确对象键；不对桶或前缀执行递归删除。
    if not channel_root.endswith("/") or not keep_keys or any(
            not key.startswith(channel_root) for key in keep_keys):
        raise ValueError("R2 清理保留列表越出当前通道")
    keys = list_channel_objects(bucket, endpoint, channel_root)
    if not keep_keys <= keys:
        raise ValueError("R2 最新版本对象缺失，拒绝清理")
    if get_json(latest_url) != manifest:
        raise ValueError("公开入口已变化，拒绝清理旧版本")
    obsolete = sorted(keys - keep_keys)
    for key in obsolete:
        print(f"删除 R2 旧对象: {key}")
        subprocess.run(["aws", "s3api", "delete-object", "--bucket", bucket, "--key", key,
                        "--endpoint-url", endpoint, "--region", "auto", "--output", "json"],
                       check=True, capture_output=True)
    if list_channel_objects(bucket, endpoint, channel_root) != keep_keys:
        raise ValueError("R2 清理后对象列表与保留列表不一致，请重跑发布")
    print(f"R2 通道清理完成: 前缀={channel_root} 保留版本={manifest['version']} "
          f"删除对象={len(obsolete)} 保留对象={len(keep_keys)}")
    return len(obsolete)


def publish(*, output_dir: Path, bucket: str, prefix: str, endpoint: str,
            public_base_url: str) -> dict:
    output_dir = Path(output_dir)
    manifest = json.loads((output_dir / "release.json").read_text(encoding="utf-8"))
    prefix, base = metadata(manifest["version"], manifest["channel"], manifest["commit"], prefix, public_base_url)
    timestamp(manifest["published_at"])
    if manifest.get("schema_version") != 1 or manifest.get("project") != "slavesim":
        raise ValueError("发布清单项目或 schema_version 不合法")
    if set(manifest.get("platforms", {})) != {"linux-" + arch for arch in ARCHES}:
        raise ValueError("发布清单必须包含 amd64 和 arm64 两个架构")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", bucket):
        raise ValueError("R2 存储桶名称不合法")
    # endpoint 独立于公开地址，允许 S3 兼容服务路径，但不允许凭据或明文连接。
    parsed = urlsplit(endpoint)
    try:
        parsed.port
    except ValueError as exc:
        raise ValueError("R2 endpoint 端口不合法") from exc
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment or "\\" in endpoint
            or any(char.isspace() or ord(char) < 32 for char in endpoint)):
        raise ValueError("R2 endpoint 必须是无凭据的 HTTPS 地址")
    asset_version = artifact_version(manifest["version"])
    version_key = f"{prefix}/{manifest['channel']}/{asset_version}"
    immutable_url = encoded_url(base, version_key + "/release.json")
    assets = []
    for arch in ARCHES:
        entry = manifest["platforms"]["linux-" + arch]
        if entry.get("image") != image_name(manifest["version"], arch) or not re.fullmatch(r"sha256:[0-9a-f]{64}", entry.get("image_id", "")):
            raise ValueError(f"{arch} 镜像身份不合法")
        for binary in (False, True):
            marker = "binary_" if binary else ""
            suffix = ".tar.gz" if binary else "-image.tar.gz"
            name = f"slavesim-{asset_version}-linux-{arch}{suffix}"
            key = f"{version_key}/{arch}/{name}"
            path = output_dir / "assets" / asset_version / arch / name
            sha, size = entry[marker + "sha256"], entry[marker + "size"]
            if (entry[marker + "url"] != encoded_url(base, key) or not HEX64.fullmatch(sha)
                    or type(size) is not int or size <= 0 or not path.is_file()
                    or path.stat().st_size != size or digest(path) != sha):
                raise ValueError(f"本地发布资产或 URL 校验失败: {name}")
            assets.append((path, key, entry[marker + "url"], sha, size, "application/gzip"))
    checksums = output_dir / "SHA256SUMS"
    expected_checksums = "\n".join(f"{sha}  {key.removeprefix(version_key + '/')}" for _, key, _, sha, _, _ in assets) + "\n"
    if checksums.read_text(encoding="utf-8") != expected_checksums:
        raise ValueError("SHA256SUMS 与发布清单不一致")
    assets.append((checksums, version_key + "/SHA256SUMS", encoded_url(base, version_key + "/SHA256SUMS"),
                   digest(checksums), checksums.stat().st_size, "text/plain; charset=utf-8"))
    existing = get_json(immutable_url, missing_ok=True)
    if existing is not None and not same_release(existing, manifest):
        raise ValueError("拒绝覆盖远端相同版本下的不同提交或发布资产")
    latest_key = f"{prefix}/{manifest['channel']}/latest.json"
    current = get_json(encoded_url(base, latest_key), missing_ok=True)
    if current is not None:
        if (current.get("schema_version") != 1 or current.get("project") != "slavesim"
                or current.get("channel") != manifest["channel"]):
            raise ValueError("远端入口清单不属于当前项目或通道，拒绝覆盖")
        new_order = version_order(manifest["channel"], manifest["version"])
        current_order = version_order(current["channel"], current.get("version", ""))
        if new_order < current_order:
            raise ValueError("拒绝让 latest.json 版本倒退")
        if new_order == current_order and not same_release(current, manifest):
            raise ValueError("拒绝覆盖入口清单中相同版本的不同内容")
    immutable_cache = "public,max-age=31536000,immutable"
    if existing is None:
        for path, key, _, _, _, content_type in assets:
            upload(path, key, bucket, endpoint, content_type, immutable_cache)
    for _, _, url, sha, size, _ in assets:
        verify_asset(url, sha, size)
    if existing is None:
        upload(output_dir / "release.json", version_key + "/release.json", bucket, endpoint,
               "application/json; charset=utf-8", immutable_cache)
        verify_asset(immutable_url, digest(output_dir / "release.json"), (output_dir / "release.json").stat().st_size)
    else:
        manifest = existing
    # latest 仅在全部版本资产公开可下载且完整后更新，验证入口后才清理旧版本。
    with tempfile.TemporaryDirectory(prefix="slavesim-latest-") as temporary:
        latest = Path(temporary) / "latest.json"
        latest.write_bytes(json_bytes(manifest))
        upload(latest, latest_key, bucket, endpoint, "application/json; charset=utf-8", "no-store")
        if get_json(encoded_url(base, latest_key)) != manifest:
            raise ValueError("公开 latest.json 与发布清单不一致")
    cleanup_old_versions(bucket=bucket, endpoint=endpoint,
                         channel_root=f"{prefix}/{manifest['channel']}/",
                         keep_keys={latest_key, version_key + "/release.json", *(key for _, key, *_ in assets)},
                         manifest=manifest, latest_url=encoded_url(base, latest_key))
    print(f"R2 发布完成: {encoded_url(base, latest_key)}")
    return manifest


def main() -> None:
    parser = argparse.ArgumentParser(description="slaveSim 双架构静态发布")
    commands = parser.add_subparsers(dest="command", required=True)
    pack = commands.add_parser("prepare", help="生成发布包和清单")
    pack.add_argument("--input-dir", type=Path, default=Path("dist"))
    pack.add_argument("--output-dir", type=Path, default=Path("release"))
    for name in ("version", "channel", "commit", "public-base-url"):
        pack.add_argument("--" + name, required=True)
    pack.add_argument("--prefix", default="slavesim")
    pack.add_argument("--published-at")
    send = commands.add_parser("publish", help="发布版本资产和最新清单到 R2")
    send.add_argument("--output-dir", type=Path, default=Path("release"))
    send.add_argument("--bucket", required=True)
    send.add_argument("--prefix", default="slavesim")
    connection = send.add_mutually_exclusive_group(required=True)
    connection.add_argument("--endpoint")
    connection.add_argument("--account-id")
    send.add_argument("--public-base-url", required=True)
    args = vars(parser.parse_args())
    command = args.pop("command")
    try:
        if command == "prepare":
            prepare(**args)
        else:
            account = args.pop("account_id")
            if account is not None:
                if not re.fullmatch(r"[0-9a-f]{32}", account):
                    raise ValueError("R2 account-id 必须是 32 位小写十六进制")
                args["endpoint"] = f"https://{account}.r2.cloudflarestorage.com"
            publish(**args)
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"发布失败: {exc}\n")


if __name__ == "__main__":
    main()

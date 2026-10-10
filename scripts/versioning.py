#!/usr/bin/env python3
"""发布与设备更新共用的版本编号、身份校验和镜像标签规则。"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
from pathlib import Path
import re

SEMVER = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
LEGACY_CI = re.compile(r"0\.0\.0-ci\.([1-9][0-9]*)\.([1-9][0-9]*)")
CI = re.compile(rf"({SEMVER})-ci\.([0-9]{{8}}t[0-9]{{6}})\+sha\.([0-9a-f]{{7}})")


def version_order(channel: str, version: str) -> tuple[int, ...]:
    """新 CI 排在旧格式之后；SHA 只表示来源，不参与版本先后比较。"""
    if channel == "stable" and re.fullmatch(SEMVER, version):
        return tuple(map(int, version.split(".")))
    if channel == "ci":
        legacy = LEGACY_CI.fullmatch(version)
        if legacy:
            return (0, *map(int, legacy.groups()))
        current = CI.fullmatch(version)
        if current:
            try:
                datetime.strptime(current[2], "%Y%m%dt%H%M%S")
            except ValueError as exc:
                raise ValueError("CI 版本包含无效的 UTC 日期时间") from exc
            return (1, *map(int, current[1].split(".")), int(current[2].replace("t", "")))
    raise ValueError("版本号与发布通道不匹配：" + str(version))


def validate_commit(channel: str, version: str, commit: str) -> None:
    version_order(channel, version)
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("来源提交必须是 40 位小写十六进制 SHA")
    current = CI.fullmatch(version) if channel == "ci" else None
    if current and current[3] != commit[:7]:
        raise ValueError("CI 版本中的短提交号与来源提交不一致")


def artifact_version(version: str) -> str:
    """路径与 Docker 标签使用下划线代替加号，展示版本保持原样。"""
    return version.replace("+", "_")


def image_name(version: str, arch: str) -> str:
    if arch not in ("amd64", "arm64"):
        raise ValueError("镜像架构必须为 amd64 或 arm64")
    tag = artifact_version(version) + "-" + arch
    if not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", tag):
        raise ValueError("镜像标签包含非法字符或长度超过 128")
    return "slavesim:" + tag


def read_base_version(path: Path) -> str:
    version = path.read_text(encoding="utf-8").strip()
    if not re.fullmatch(SEMVER, version):
        raise ValueError("VERSION 必须是无前导零的 X.Y.Z 基础版本")
    return version


def build_metadata(channel: str, commit: str, base_version: str,
                   timestamp: str | None = None, tag: str | None = None) -> dict:
    if not re.fullmatch(SEMVER, base_version):
        raise ValueError("基础版本必须为 X.Y.Z")
    try:
        built = datetime.fromisoformat(timestamp.replace("Z", "+00:00")) if timestamp else datetime.now(timezone.utc)
        if built.tzinfo is None:
            raise ValueError("缺少时区")
        built = built.astimezone(timezone.utc).replace(microsecond=0)
    except ValueError as exc:
        raise ValueError("构建时间必须是包含时区的有效 ISO8601 时间") from exc
    if channel == "stable":
        if tag != "v" + base_version:
            raise ValueError("正式版本标签必须与 VERSION 中的基础版本完全一致")
        version = base_version
    elif channel == "ci":
        version = f"{base_version}-ci.{built:%Y%m%dt%H%M%S}+sha.{commit[:7]}"
    else:
        raise ValueError("发布通道必须为 ci 或 stable")
    validate_commit(channel, version, commit)
    image_name(version, "amd64")
    return dict(channel=channel, version=version, commit=commit,
                published_at=built.isoformat().replace("+00:00", "Z"), image_tag=artifact_version(version))


def main() -> None:
    parser = argparse.ArgumentParser(description="生成 slaveSim CI 版本元数据")
    parser.add_argument("--commit", required=True, help="完整来源提交 SHA")
    parser.add_argument("--github-output", type=Path, help="GitHub Actions 步骤输出文件")
    args = parser.parse_args()
    try:
        base = read_base_version(Path(__file__).resolve().parents[1] / "VERSION")
        data = build_metadata("ci", args.commit, base)
        if args.github_output:
            with args.github_output.open("a", encoding="utf-8", newline="\n") as output:
                for key, value in data.items():
                    output.write(f"{key}={value}\n")
        print(f"构建元数据：版本={data['version']} 提交={data['commit']} 时间={data['published_at']} 镜像标签={data['image_tag']}")
    except (ValueError, OSError) as exc:
        parser.exit(1, "生成版本元数据失败：" + str(exc) + "\n")


if __name__ == "__main__":
    main()

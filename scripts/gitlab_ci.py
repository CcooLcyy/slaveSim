#!/usr/bin/env python3
"""运行 GitLab 双架构持续集成，复用版本规则与实际运行镜像的 PTY 验证。"""

from __future__ import annotations

import argparse
import base64
from contextlib import contextmanager
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

from versioning import build_metadata, image_name, read_base_version, validate_commit

ROOT = Path(__file__).resolve().parents[1]
ARCHES = ("amd64", "arm64")


def target_architecture(env: dict) -> str:
    arch = env.get("TARGETARCH", "amd64")
    if arch not in ARCHES:
        raise ValueError("不支持的 CI 目标架构：" + str(arch))
    return arch


def verify_binary(path: Path, arch: str) -> None:
    # 构建信息可跨架构读取；实际执行与版本输出检查由目标镜像完成。
    output = subprocess.check_output(["go", "version", "-m", str(path)], text=True)
    settings = {}
    for line in output.splitlines():
        fields = line.strip().split("\t", 1)
        if len(fields) == 2 and fields[0] == "build":
            key, separator, value = fields[1].partition("=")
            if separator:
                settings[key] = value
    expected = dict(GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    if any(settings.get(key) != value for key, value in expected.items()):
        raise ValueError("二进制目标平台与 CI 架构不一致：" + path.name)


def create_metadata(commit: str, output: Path) -> dict:
    data = build_metadata("ci", commit, read_base_version(ROOT / "VERSION"))
    values = dict(VERSION=data["version"], COMMIT=data["commit"], BUILD_TIME=data["published_at"],
                  IMAGE_TAG=data["image_tag"], CHANNEL=data["channel"])
    output.write_text("".join(f"{key}={value}\n" for key, value in values.items()), encoding="utf-8", newline="\n")
    print(f"CI 版本已生成：{data['version']}，来源提交：{commit}", flush=True)
    return data


def load_metadata(env: dict) -> dict:
    names = ("VERSION", "COMMIT", "BUILD_TIME", "IMAGE_TAG", "CHANNEL", "CI_COMMIT_SHA")
    missing = [name for name in names if not env.get(name)]
    if missing:
        raise ValueError("缺少 CI 元数据：" + ", ".join(missing))
    if env["COMMIT"] != env["CI_COMMIT_SHA"]:
        raise ValueError("元数据提交号与流水线提交不一致")
    validate_commit(env["CHANNEL"], env["VERSION"], env["COMMIT"])
    base_version = read_base_version(ROOT / "VERSION")
    expected = build_metadata("ci", env["COMMIT"], base_version, timestamp=env["BUILD_TIME"])
    data = dict(channel=env["CHANNEL"], version=env["VERSION"], commit=env["COMMIT"],
                published_at=env["BUILD_TIME"], image_tag=env["IMAGE_TAG"])
    if data != expected:
        raise ValueError("CI 版本、时间、通道或镜像标签不一致")
    return data


def checksums(folder: Path) -> None:
    files = sorted(path for path in folder.iterdir() if path.is_file() and path.name != "SHA256SUMS")
    (folder / "SHA256SUMS").write_text("".join(
        f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in files), encoding="utf-8")


def build_binaries(root: Path, env: dict) -> dict:
    arch = target_architecture(env)
    data = load_metadata(env)
    folder = root / "dist" / arch
    folder.mkdir(parents=True, exist_ok=True)
    build_env = dict(env, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOTOOLCHAIN="local")
    flags = f"-s -w -X main.version={data['version']} -X main.commit={data['commit']} -X main.buildTime={data['published_at']}"
    print(f"构建 linux/{arch} 主程序与协议探针", flush=True)
    subprocess.run(["go", "build", "-trimpath", "-ldflags", flags, "-o", str(folder / "slavesim"), "."],
                   cwd=root, env=build_env, check=True)
    subprocess.run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", str(folder / "probe"), "./cmd/probe"],
                   cwd=root, env=build_env, check=True)
    verify_binary(folder / "slavesim", arch)
    verify_binary(folder / "probe", arch)
    print(f"linux/{arch} 二进制架构校验通过，版本身份将在目标镜像内验证", flush=True)
    (folder / "metadata.json").write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    checksums(folder)
    return data


def make_ci_dockerfile(source: str) -> str:
    # 临时构建直接使用本次二进制，确保镜像验证覆盖最终发布包中的同一份程序。
    marker = "FROM ${PYTHON_IMAGE}"
    if source.count(marker) != 1:
        raise ValueError("运行镜像阶段无法唯一识别，拒绝跳过镜像验证")
    source = source.replace(marker, marker + " AS ci-runtime\nARG TARGETARCH", 1)
    for name in ("slavesim", "probe"):
        original = f"COPY --from=builder /out/{name} /{name}"
        if source.count(original) != 1:
            raise ValueError("运行镜像二进制来源无法唯一识别：" + name)
        source = source.replace(original, f"COPY dist/${{TARGETARCH}}/{name} /{name}", 1)
    return source + """

# 验证实际运行层中的管理进程、网页和两个站点的真实 PTY 收发。
FROM ci-runtime AS ci-smoke
ARG CI_SMOKE_NONCE
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
ARG TARGETARCH
COPY scripts/smoke_runtime.py /ci-smoke-runtime.py
RUN test -n "$CI_SMOKE_NONCE" && /slavesim -version \\
    && CI_EXPECTED_VERSION="$VERSION" CI_EXPECTED_COMMIT="$COMMIT" \\
       CI_EXPECTED_BUILD_TIME="$BUILD_TIME" CI_EXPECTED_ARCH="$TARGETARCH" \\
       python3 -B /ci-smoke-runtime.py && printf '%s\\n' "$CI_SMOKE_NONCE" > /ci-smoke-ok

FROM scratch AS ci-smoke-result
COPY --from=ci-smoke /ci-smoke-ok /ci-smoke-ok
"""


@contextmanager
def buildkit_client(env: dict):
    names = ("BUILDKIT_HOST", "BUILDKIT_CA_B64", "BUILDKIT_CERT_B64", "BUILDKIT_KEY_B64")
    missing = [name for name in names if not env.get(name)]
    if missing:
        raise ValueError("缺少 BuildKit 配置：" + ", ".join(missing))
    if not re.fullmatch(r"tcp://[A-Za-z0-9.-]+:[0-9]+", env["BUILDKIT_HOST"]):
        raise ValueError("BuildKit 必须使用配置了 mTLS 的 TCP 地址")
    with tempfile.TemporaryDirectory(prefix="slavesim-buildkit-") as directory:
        folder = Path(directory)
        os.chmod(folder, 0o700)
        for key, name in (("BUILDKIT_CA_B64", "ca.pem"), ("BUILDKIT_CERT_B64", "cert.pem"), ("BUILDKIT_KEY_B64", "key.pem")):
            try:
                content = base64.b64decode(env[key], validate=True)
            except ValueError as exc:
                raise ValueError("BuildKit 证书编码不合法：" + key) from exc
            path = folder / name
            path.write_bytes(content)
            os.chmod(path, 0o600)
        yield ["buildctl", "--addr", env["BUILDKIT_HOST"], "--tlscacert", str(folder / "ca.pem"),
               "--tlscert", str(folder / "cert.pem"), "--tlskey", str(folder / "key.pem")], folder


def run_logged(command: list[str], log: Path, root: Path) -> None:
    # 不输出环境变量或私钥，终端和产物仅保留构建、运行验证日志。
    with log.open("w", encoding="utf-8") as stream:
        process = subprocess.Popen(command, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                   text=True, encoding="utf-8", errors="replace")
        for line in process.stdout:
            print(line, end="", flush=True)
            stream.write(line)
        if process.wait():
            raise subprocess.CalledProcessError(process.returncode, command)


def build_image(root: Path, env: dict) -> None:
    arch = target_architecture(env)
    data = load_metadata(env)
    folder = root / "dist" / arch
    if not (folder / "metadata.json").is_file() or json.loads((folder / "metadata.json").read_text(encoding="utf-8")) != data:
        raise ValueError("缺少同一提交已经测试并构建的二进制元数据")
    for name in ("slavesim", "probe"):
        if not (folder / name).is_file():
            raise ValueError("缺少二进制产物：" + name)
    nonce = env.get("CI_JOB_ID")
    if not nonce or not nonce.isdigit():
        raise ValueError("缺少合法的 CI_JOB_ID，无法保证镜像验证重新执行")
    with buildkit_client(env) as (client, temporary):
        # 独立 ignore 文件仅增加 smoke 脚本；不改变生产镜像的构建上下文规则。
        dockerfile_dir = temporary / "dockerfile"
        dockerfile_dir.mkdir()
        (dockerfile_dir / "Dockerfile").write_text(make_ci_dockerfile((root / "Dockerfile").read_text(encoding="utf-8")), encoding="utf-8")
        ignore = (root / ".dockerignore").read_text(encoding="utf-8")
        (dockerfile_dir / "Dockerfile.dockerignore").write_text(
            ignore + f"\n!scripts/smoke_runtime.py\n!dist\n!dist/{arch}\n"
                     f"!dist/{arch}/slavesim\n!dist/{arch}/probe\n", encoding="utf-8")
        epoch = subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], cwd=root, text=True).strip()
        common = client + ["build", "--progress", "plain", "--frontend", "dockerfile.v0",
                           "--local", f"context={root}", "--local", f"dockerfile={dockerfile_dir}",
                           "--opt", f"platform=linux/{arch}", "--opt", f"build-arg:VERSION={data['version']}",
                           "--opt", f"build-arg:COMMIT={data['commit']}",
                           "--opt", f"build-arg:BUILD_TIME={data['published_at']}",
                           "--opt", f"build-arg:SOURCE_DATE_EPOCH={epoch}",
                           "--opt", "build-arg:BUILDKIT_SYNTAX=docker.m.daocloud.io/docker/dockerfile:1"]
        for key in ("GO_IMAGE", "PYTHON_IMAGE"):
            if env.get(key):
                common += ["--opt", f"build-arg:{key}={env[key]}"]
        result = temporary / "smoke"
        print(f"在 linux/{arch} 实际运行镜像环境中验证版本、管理进程、网页与 PTY", flush=True)
        run_logged(common + ["--opt", "target=ci-smoke-result", "--opt", f"build-arg:CI_SMOKE_NONCE={nonce}",
                             "--output", f"type=local,dest={result}"], folder / "smoke-runtime.log", root)
        if (result / "ci-smoke-ok").read_text().strip() != nonce:
            raise ValueError("镜像运行验证标记不一致")
        image = image_name(data["version"], arch)
        archive = temporary / "image.tar"
        run_logged(common + ["--opt", "target=ci-runtime", "--output", f"type=docker,name={image},dest={archive}"],
                   folder / "image-build.log", root)
        with archive.open("rb") as source, (folder / "slavesim-image.tar.gz").open("wb") as destination:
            with gzip.GzipFile(filename="", mode="wb", fileobj=destination, mtime=0) as compressed:
                shutil.copyfileobj(source, compressed)
        identity = subprocess.check_output([sys.executable, str(root / "scripts/update.py"), "image-id",
                                            "--archive", str(folder / "slavesim-image.tar.gz"), "--image", image],
                                           text=True).strip()
        (folder / "image-id.txt").write_text(identity + "\n", encoding="utf-8")
    checksums(folder)
    print(f"linux/{arch} 镜像构建与运行验证通过：{image}，{identity}", flush=True)


def publish_release(root: Path, env: dict) -> None:
    if (env.get("CI_COMMIT_BRANCH") != "master" or env.get("CI_COMMIT_REF_PROTECTED") != "true"
            or env.get("CI_PIPELINE_SOURCE") not in ("push", "web")):
        raise ValueError("R2 发布仅允许受保护 master 的推送或手动流水线")
    data = load_metadata(env)
    names = ("R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY")
    missing = [name for name in names if not env.get(name)]
    if missing:
        raise ValueError("缺少 R2 发布凭据：" + ", ".join(missing))
    for arch in ARCHES:
        folder = root / "dist" / arch
        required = {"slavesim", "probe", "slavesim-image.tar.gz", "image-id.txt", "metadata.json"}
        if any(not (folder / name).is_file() for name in required | {"SHA256SUMS"}):
            raise ValueError(f"缺少 {arch} 已验证的发布输入")
        if json.loads((folder / "metadata.json").read_text(encoding="utf-8")) != data:
            raise ValueError(f"{arch} 待发布产物与本次流水线元数据不一致")
        verified = set()
        for line in (folder / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)", line)
            if not match or match[2] in verified:
                raise ValueError("构建产物校验和格式不合法或文件重复")
            checksum, name = match.groups()
            path = folder / name
            if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != checksum:
                raise ValueError(f"{arch} 构建产物 SHA256 校验失败：" + name)
            verified.add(name)
        if not required <= verified:
            raise ValueError(f"{arch} 缺少已校验的发布输入：" + ", ".join(sorted(required - verified)))
        print(f"linux/{arch} 发布输入与 SHA256 校验通过", flush=True)
    bucket = env.get("R2_BUCKET") or "mskdsp-update"
    prefix = env.get("R2_PREFIX") or "slavesim"
    base = env.get("R2_PUBLIC_BASE_URL") or "https://pub-19f3d71852b04011b120b1b814141c12.r2.dev"
    publish_env = dict(env, AWS_ACCESS_KEY_ID=env["R2_ACCESS_KEY_ID"],
                       AWS_SECRET_ACCESS_KEY=env["R2_SECRET_ACCESS_KEY"], AWS_DEFAULT_REGION="auto",
                       AWS_REQUEST_CHECKSUM_CALCULATION="when_required", AWS_RESPONSE_CHECKSUM_VALIDATION="when_required")
    common = [sys.executable, str(root / "scripts/release.py")]
    options = ["--output-dir", str(root / "release"), "--architectures", *ARCHES,
               "--prefix", prefix, "--public-base-url", base]
    print(f"校验通过，沿用现有发布工具上传 R2：{prefix}/ci，版本：{data['version']}", flush=True)
    subprocess.run(common + ["prepare", "--input-dir", str(root / "dist"), "--version", data["version"],
                             "--channel", "ci", "--commit", data["commit"], "--published-at", data["published_at"]]
                   + options, cwd=root, env=publish_env, check=True)
    subprocess.run(common + ["publish", "--bucket", bucket, "--account-id", env["R2_ACCOUNT_ID"]]
                   + options, cwd=root, env=publish_env, check=True)
    print(f"GitLab R2 发布完成：{base.rstrip('/')}/{prefix}/ci/latest.json", flush=True)


def main() -> None:
    parser = argparse.ArgumentParser(description="slaveSim 双架构 GitLab CI")
    commands = parser.add_subparsers(dest="command", required=True)
    metadata = commands.add_parser("metadata", help="生成本次流水线共用元数据")
    metadata.add_argument("--output", type=Path, required=True)
    commands.add_parser("build", help="按 TARGETARCH 构建二进制")
    commands.add_parser("image", help="按 TARGETARCH 构建并验证运行镜像")
    commands.add_parser("publish", help="将已验证的双架构产物统一发布到 R2 测试通道")
    args = parser.parse_args()
    try:
        if args.command == "metadata":
            create_metadata(os.environ.get("CI_COMMIT_SHA", ""), args.output)
        elif args.command == "build":
            build_binaries(ROOT, dict(os.environ))
        elif args.command == "image":
            build_image(ROOT, dict(os.environ))
        else:
            publish_release(ROOT, dict(os.environ))
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"GitLab CI 执行失败：{exc}\n")


if __name__ == "__main__":
    main()

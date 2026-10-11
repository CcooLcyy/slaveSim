# syntax=docker/dockerfile:1

# 构建阶段镜像可用 --build-arg 覆盖，便于在受限网络下切换镜像源。
ARG GO_IMAGE=docker.m.daocloud.io/library/golang:1.24-alpine
ARG PYTHON_IMAGE=python:3.12-alpine

# 构建阶段固定运行在构建机架构上（$BUILDPLATFORM），再由 Go 交叉编译到目标架构
# （$TARGETARCH）。运行阶段使用目标架构 Python 镜像且不含任何 RUN 指令，交叉构建
# arm64 镜像不需要 qemu 模拟，构建速度快且结果确定。
#
#   docker build --platform linux/arm64 -t slavesim:arm64 .
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

ARG TARGETARCH
ARG VERSION
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /src

# 项目不依赖任何第三方包，因此无需 go mod download，构建过程完全离线。
COPY go.mod ./
COPY main.go ./
COPY version.go ./
COPY VERSION ./
COPY internal ./internal
COPY cmd ./cmd
COPY configs/inverters ./configs/inverters

RUN version=${VERSION:-$(cat VERSION)-dev} \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags "-s -w -X main.version=${version} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" -o /out/slavesim . \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags "-s -w" -o /out/probe ./cmd/probe

# 管理进程常驻容器，更新只重启静态 Go 子进程；基础镜像独立维护。
# 此阶段无 RUN 指令，仍可直接交叉构建 arm64，不需要 qemu。
FROM ${PYTHON_IMAGE}

COPY --from=builder /out/slavesim /slavesim
# 主站探针一并放入镜像，便于在设备上直接自检收发链路。
COPY --from=builder /out/probe /probe
COPY config.example.json /etc/slavesim/config.example.json
COPY config.inverters.example.json /opt/slavesim/examples/config.inverters.example.json
COPY configs/inverters /opt/slavesim/examples/inverters
COPY scripts/runtime.py scripts/update.py scripts/versioning.py /app/
COPY deploy/updater.runtime.example.json /etc/slavesim/updater.json

ENV TZ=Asia/Shanghai
EXPOSE 16000

ENTRYPOINT ["python3", "-B", "/app/runtime.py"]
CMD ["-config", "/etc/slavesim/config.json", "-http", ":16000"]

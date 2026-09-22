# syntax=docker/dockerfile:1

# 构建阶段镜像可用 --build-arg 覆盖，便于在受限网络下切换镜像源。
ARG GO_IMAGE=docker.m.daocloud.io/library/golang:1.24-alpine

# 构建阶段固定运行在构建机架构上（$BUILDPLATFORM），再由 Go 交叉编译到目标架构
# （$TARGETARCH）。因为运行阶段是 scratch 且不含任何 RUN 指令，所以交叉构建
# arm64 镜像不需要 qemu 模拟，构建速度快且结果确定。
#
#   docker build --platform linux/arm64 -t slavesim:arm64 .
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

ARG TARGETARCH

WORKDIR /src

# 项目不依赖任何第三方包，因此无需 go mod download，构建过程完全离线。
COPY go.mod ./
COPY main.go ./
COPY internal ./internal
COPY cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags "-s -w" -o /out/slavesim . \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags "-s -w" -o /out/probe ./cmd/probe

# 运行阶段用空镜像：二进制是静态的，时区库已通过 time/tzdata 编入。
FROM scratch

COPY --from=builder /out/slavesim /slavesim
# 主站探针一并放入镜像，便于在设备上直接自检收发链路。
COPY --from=builder /out/probe /probe
COPY config.example.json /etc/slavesim/config.example.json

ENV TZ=Asia/Shanghai
EXPOSE 16000

ENTRYPOINT ["/slavesim"]
CMD ["-config", "/etc/slavesim/config.json", "-http", ":16000"]

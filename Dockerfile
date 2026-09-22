# 构建阶段镜像可用 --build-arg 覆盖，便于在受限网络下切换镜像源。
ARG GO_IMAGE=docker.m.daocloud.io/library/golang:1.24-alpine

FROM ${GO_IMAGE} AS builder

WORKDIR /src

# 项目不依赖任何第三方包，因此无需 go mod download，构建过程完全离线。
COPY go.mod ./
COPY main.go ./
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/slavesim .

# 运行阶段用空镜像：二进制是静态的，时区库已通过 time/tzdata 编入。
FROM scratch

COPY --from=builder /out/slavesim /slavesim
COPY config.example.json /etc/slavesim/config.example.json

ENV TZ=Asia/Shanghai
EXPOSE 16000

ENTRYPOINT ["/slavesim"]
CMD ["-config", "/etc/slavesim/config.json", "-http", ":16000"]

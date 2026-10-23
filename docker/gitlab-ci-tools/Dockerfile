# 构建与测试工具独立于下位机镜像，升级时使用新的工具镜像标签。
ARG GO_IMAGE=docker.m.daocloud.io/library/golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac
ARG NODE_IMAGE=docker.m.daocloud.io/library/node:22-bookworm-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d
ARG BUILDKIT_IMAGE=docker.m.daocloud.io/moby/buildkit@sha256:65eca3c01cd1180c8f64565c4e3c5983af5145818b0e6fa863385d099eb73855

FROM ${NODE_IMAGE} AS node
FROM ${BUILDKIT_IMAGE} AS buildkit
FROM ${GO_IMAGE}

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
RUN apt-get -o Acquire::Retries=3 update \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends python3 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && go version && node --version && python3 --version && gcc --version && buildctl --version

ENV GOTOOLCHAIN=local
WORKDIR /workspace

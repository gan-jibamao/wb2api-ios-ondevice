FROM golang:1.26-alpine AS build
# 国内网络：Go 模块代理
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=off
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# 一次编译全部二进制（工具进镜像，容器内可直接跑脚本）。全部 -trimpath -s -w。
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wb2api ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/signin_bin ./cmd/signin \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/login ./cmd/login \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/credit ./cmd/credit \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/trial_bin ./cmd/trial \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/activity_bin ./cmd/activity

FROM alpine:3.20
# 国内 Alpine 镜像源
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.aliyun.com#g' /etc/apk/repositories
# python3：login.sh 的 JSON 解析 / 签到 / 落盘；bash：shell 脚本体。
RUN apk add --no-cache wget ca-certificates tzdata python3 bash \
 && adduser -D -u 10001 app \
 && mkdir -p /app/auths /app/data \
 && chown -R app:app /app
WORKDIR /app
COPY --from=build /out/wb2api /app/wb2api
COPY --from=build /out/signin_bin /app/signin_bin
COPY --from=build /out/login /app/login
COPY --from=build /out/credit /app/credit
COPY --from=build /out/trial_bin /app/trial_bin
COPY --from=build /out/activity_bin /app/activity_bin
COPY login.sh signin.sh credit.sh trial.sh /app/
COPY scripts/global_region.py /app/scripts/global_region.py
COPY scripts/task_common.py /app/scripts/task_common.py
COPY scripts/task_runner.py /app/scripts/task_runner.py
COPY scripts/school_open_day_2026.py /app/scripts/school_open_day_2026.py
RUN sed -i 's/\r$//' /app/*.sh && chmod 755 /app/*.sh
RUN sed -i 's/\r$//' /app/scripts/*.py && chmod 755 /app/scripts/*.py
COPY config.example.json /app/config.json
USER app
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/app/wb2api", "-config", "/app/config.json"]

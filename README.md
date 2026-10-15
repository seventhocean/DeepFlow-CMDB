# DeepFlow-CMDB

将 CMDB 中的应用归属关系同步为 DeepFlow 业务服务，同时提供 Go 命令行程序与 Python 单文件脚本。
Go 版可以编译成 Linux 可执行文件，服务器上无需 Go 或 Python 环境。
Python 版的全部运行逻辑位于 `sync_biz_service.py`，运行时只需分发这一个脚本，并安装 PyMySQL。

## Go 版构建与运行

构建使用 Go 1.26.2 或以上版本。在项目目录执行：

```bash
go version
go test ./...
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/sync-biz-service-linux-amd64 .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o dist/sync-biz-service-darwin-arm64 .
```

如果 `proxy.golang.org` 连接超时，可在构建或测试命令前临时加
`GOPROXY=https://goproxy.cn`，依赖仍通过项目 `go.sum` 校验。

将 `dist/sync-biz-service-linux-amd64` 复制到 Linux x86_64 服务器，使用原来的参数方式运行：

```bash
chmod +x ./sync-biz-service-linux-amd64
./sync-biz-service-linux-amd64 \
  -db-host 127.0.0.1 \
  -db-port 30130 \
  -database deepflow \
  -db-user-name root \
  -db-user-pswd '数据库密码' \
  -http-host dlink-api.qa.pab.com.cn \
  -http-header 'Authorization:正确的完整Token' \
  -log-debug
```

Go 版保留原有日志方式：完整启动配置和 HTTP 请求头写入可执行文件同目录的
`sync-biz-service.log`，可以通过 `-log-path` 覆盖。`-log-debug` 直接作为开关使用，后面不加 `1`。
Go 版沿用原来的执行方式，会直接同步数据库；下文 Python 版的 `--dry-run` 和 `--skip-delete`
不适用于 Go 版。

本次 Go 修正包括：分页 URL 独立构造、异常列表和重复分页报错、按 `(type, name)` 区分服务、
更新失败向上传递、同步更新 VPC / 集群 / 命名空间范围，以及显式 gzip 请求头的响应解压。
原有删除条件与业务归属规则保留，Token 通过 `-http-header 'Authorization:正确的完整Token'`
传入，源码不内置真实 Token。
现有同类型同名称重复服务会报错停止，避免任意选择更新对象。

接口路径仍采用原 Go 源码中的值，工作负载路径中的 `vl`、`worklaod` 未根据拼写猜测修改。
真实 CMDB 接口与 MySQL 表结构需要在部署环境联调；本地 Go 测试使用模拟仓库和本地 HTTP 服务，
不会连接业务数据库。

## Python 版安装

需要 Python 3.7 或以上版本，以及到 CMDB 和 DeepFlow MySQL 的网络访问权限。

```bash
python3 -m pip install 'PyMySQL[rsa]>=1.1,<2'
# 或在本项目目录执行：python3 -m pip install -r requirements.txt
python3 sync_biz_service.py --help
```

`rsa` 扩展用于支持需要 RSA 的 MySQL 认证方式。HTTP 请求、JSON 处理、日志和参数解析
均使用 Python 标准库，无需 Go 环境。脚本不会建表或迁移数据库，使用已有的 DeepFlow 表结构。

## 运行

以下示例使用 Bash / Zsh。密码和令牌建议通过环境变量提供，输入时不会回显，也不写入命令历史：

```bash
printf 'MySQL password: '
read -r -s DEEPFLOW_DB_PASSWORD
printf '\n'
export DEEPFLOW_DB_PASSWORD
printf 'CMDB Authorization: '
read -r -s CMDB_AUTHORIZATION
printf '\n'
export CMDB_AUTHORIZATION
```

`CMDB_AUTHORIZATION` 填写完整的 Authorization 值；接口要求 `Bearer` 前缀时一并填写。
未设置该变量时不自动添加 Authorization 请求头。

先预览同步结果，`--log-debug` 输出每条新增、修改、删除的具体内容：

```bash
python3 sync_biz_service.py \
  --db-host 127.0.0.1 \
  --db-port 30130 \
  --database deepflow \
  --db-user-name root \
  --http-host dlink-api.qa.pab.com.cn \
  --dry-run --log-debug
```

正式执行时移除 `--dry-run`。脚本运行一轮后退出；需要定时同步时，由部署环境调度。
`--skip-delete` 可以只执行新增、更新。标准输出日志写入终端和脚本同目录的
`sync-biz-service.log`，可以使用 `--log-path /path/to/sync.log` 指定日志位置。

## 参数

保留 Go 版的参数名称，支持 `--db-host` 和 `-db-host` 两种写法。
布尔参数使用 `--log-debug`、`--dry-run`、`--skip-delete`，不带 `=true`。
这些开关后面也不需要加 `1`，例如使用 `--log-debug --dry-run`。

| 参数 | 默认值 / 作用 |
| --- | --- |
| `--database` | `deepflow` |
| `--db-host` / `--db-port` | `mysql` / `30130` |
| `--db-user-name` | `root` |
| `--db-user-pswd` | 优先读取 `DEEPFLOW_DB_PASSWORD`，否则为空 |
| `--db-timeout` | 数据库连接和读写超时，30 秒 |
| `--http-host` / `--http-port` | `dlink-api.qa.pab.com.cn` / `0`（协议默认端口） |
| `--http-scheme` | `http`，可设置为 `https` |
| `--http-timeout` | `30s`，也支持 `30`、`500ms`、`2m` 等单个单位时长 |
| `--http-header 'Key: Value'` | 自定义请求头，可重复指定；可覆盖环境变量中的 Authorization |
| `--page-size` / `--max-pages` | `100` / `10000`（包含用于判断结束的空页） |
| `--vm-path` / `--pod-group-path` | CMDB 接口路径，可包含固定查询参数 |
| `--system-id` / `--user-id` / `--team-id` | 新增服务使用的归属 ID，均为 `1`；不修改旧服务归属 |
| `--log-path` / `--log-debug` | 日志文件位置 / 输出每条变更详情 |
| `--dry-run` | 完整读取数据、生成计划，不执行写入 |
| `--skip-delete` | 本轮只新增和更新 |

接口路径沿用 Go 源码：

- 云主机：`/api/v1/query/dwd/list-pa-host-b`
- 工作负载：`/api/vl/query/dwd/list-worklaod-resource-by-cluster-id`

工作负载路径中的 `vl` 和 `worklaod` 暂时原样保留。若真实路径不同，通过
`--pod-group-path '/实际接口路径'` 覆盖；需要集群等查询参数时可使用
`--pod-group-path '/实际接口路径?clusterId=123'`，分页参数会自动追加。
路径开头的双斜杠会原样保留。如果可成功调用的 curl 使用 `http://主机//api/...`，
可用 `--vm-path '//api/v1/query/dwd/list-pa-host-b'` 对齐；分页大小通过 `--page-size` 指定。
请求头中指定 `Accept-Encoding: gzip` 时，脚本会先解压响应，再解析 JSON。

## 数据约定和同步行为

两个接口必须返回顶层 `list` 数组，分页参数为 `pageSize` 和从 `1` 开始的 `pageNum`：

```json
{"list": [{"appid": "app-001", "hostname": "vm-001"}]}
```

云主机读取 `appid` 和 `hostname`；工作负载读取 `appid` 和 `name`，字段为字符串。
无 `appid` 或没有匹配到 DeepFlow 资源的记录会跳过。请求到空 `list` 时结束；
返回缺失或类型不正确的 `list`、重复分页、无效 JSON、HTTP 错误或超过页数限制时，
整轮失败，不使用部分接口数据写入数据库。

数据库操作与 Go 版保持相同范围：

1. 读取 `vm` 和 `pod_group`，排除 `deleted_at` 非空的软删除资源。
2. 分别按名称匹配资源，按 `appid` 聚合为 `type=3`（云主机）或 `type=5`（工作负载）的服务，
   `match_type=1`。资源名称、VPC ID、集群 ID、命名空间 ID 均去重排序；命名空间 ID 为 0 时不加入。
3. 读取现有 `biz_service` 中 `type IN (3, 5) AND match_type=1` 的记录，
   按 `(type, name)` 比较新增、修改；同名但不同类型的服务独立处理。
4. 更新资源名称和对应的 VPC / 集群 / 命名空间范围，保留原记录的 ID、LCUUID、
   业务归属、标签、资源上下文和其他未管理字段。新增记录生成新的 UUID。
5. 只有目标聚合结果中已无该应用，且旧服务所有资源名称也已不在对应的 DeepFlow 资源表中时，
   才物理删除该服务。只从 CMDB 消失、但资源仍在 DeepFlow 中的服务会保留。

现有服务的读取范围与 Go 一致，不按团队或业务系统进一步筛选，因此该范围也包含手动创建的记录。
若现有数据存在相同 `(type, name)` 的多条记录（例如不同团队使用相同名称），脚本会报错停止，
需要先明确记录归属。DeepFlow 资源重名时，沿用原版保留一条记录的方式，并明确采用 ID 最小者、输出告警；
当前 CMDB 字段不足以实现跨 VPC、集群或命名空间的精确区分。

所有 HTTP 读取完成后才开启数据库事务，整轮新增、更新、删除成功后统一提交；
写入失败或中断时回滚。事务回滚依赖目标表使用 InnoDB 等事务引擎；请避免多个同步实例同时写入。
错误时退出码为 `1`，中断时为 `130`，成功或成功预览为 `0`。
默认日志完整记录解析后的启动参数（包含数据库密码），以及每次 HTTP 请求的实际 URL、
请求头（包含 Authorization / Token）和超时值，方便核对参数是否正确生效，无需开启 `--log-debug`。
通过环境变量提供的数据库密码和接口令牌也会按最终生效值记录。
HTTP 错误还会输出最终响应 URL、响应头和响应正文，方便区分网关路由与接口认证问题。

## 本地验证

测试不依赖 PyMySQL 或任何外部服务：

```bash
python3 -m unittest discover -s tests -v
```

测试覆盖分页、数据聚合、跨类型同名服务、范围字段变化、删除条件、软删除过滤、
幂等同步、批量 SQL、预览不写入和事务回滚。SQL 测试使用本地 SQLite 适配器，
验证数据流程和事务行为；目标 MySQL 的表结构、权限和实际 CMDB 响应仍需在部署环境联调。

`sync_biz_service` 是本项目的脚本模块，无需使用 pip 安装。执行同步时直接运行
`python3 sync_biz_service.py` 并追加连接参数。测试文件 `tests/test_sync_biz_service.py`
需要与项目根目录下的 `sync_biz_service.py` 一起保留；只分发主脚本即可运行同步。

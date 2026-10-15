#!/usr/bin/env python3
"""Sync CMDB application ownership to DeepFlow biz_service. Python 3.7+.

Runtime dependency: python3 -m pip install 'PyMySQL[rsa]>=1.1,<2'
Run with --dry-run to read data and preview changes without writing to MySQL.
See --help for connection parameters and README.md for behavior details.
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import logging
import math
import os
import re
import sys
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
from urllib.request import Request, urlopen


CHOST = 3
POD_GROUP = 5
MATCH_NAME = 1
VM_PATH = "/api/v1/query/dwd/list-pa-host-b"
# Keep the spelling in the Go source; override if the real API differs.
POD_GROUP_PATH = "/api/vl/query/dwd/list-worklaod-resource-by-cluster-id"
LOG = logging.getLogger("sync-biz-service")
ServiceKey = Tuple[int, str]


class SyncError(Exception):
    """An error that prevents a complete, valid sync."""


def positive_int(value: str) -> int:
    try:
        result = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("必须是正整数") from exc
    if result <= 0:
        raise argparse.ArgumentTypeError("必须是正整数")
    return result


def duration_seconds(value: str) -> float:
    """Accept seconds or a single duration unit, e.g. 30, 30s, 500ms, 2m."""
    match = re.fullmatch(r"(\d+(?:\.\d+)?)(ms|s|m|h)?", value)
    if not match:
        raise argparse.ArgumentTypeError("超时示例: 30、30s、500ms、2m")
    result = float(match[1]) * {None: 1, "ms": 0.001, "s": 1, "m": 60, "h": 3600}[match[2]]
    if not math.isfinite(result) or result <= 0:
        raise argparse.ArgumentTypeError("超时必须大于 0")
    return result


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="按 CMDB appid 同步 DeepFlow 云主机和工作负载业务服务（运行一轮后退出）。"
    )

    def option(name: str, **kwargs: Any) -> None:
        # Accept the Go program's single-dash option names as well.
        parser.add_argument("--" + name, "-" + name, **kwargs)

    option("database", default="deepflow", help="数据库名，默认 deepflow")
    option("db-host", default="mysql", help="MySQL 地址，默认 mysql")
    option("db-port", type=positive_int, default=30130, help="MySQL 端口，默认 30130")
    option("db-user-name", default="root", help="MySQL 用户，默认 root")
    option("db-user-pswd", default=os.environ.get("DEEPFLOW_DB_PASSWORD", ""),
           help="MySQL 密码，建议通过 DEEPFLOW_DB_PASSWORD 环境变量提供")
    option("db-timeout", type=positive_int, default=30, help="数据库连接和读写超时秒数，默认 30")
    option("http-host", default="dlink-api.qa.pab.com.cn", help="CMDB 主机名，不含协议和路径")
    option("http-port", type=int, default=0, help="CMDB 端口，0 表示使用协议默认端口")
    option("http-scheme", choices=("http", "https"), default="http", help="默认 http，与 Go 一致")
    option("http-timeout", type=duration_seconds, default=30.0, help="HTTP 超时，默认 30s")
    option("http-header", action="append", default=[], metavar="KEY:VALUE", help="请求头，可重复指定")
    option("page-size", type=positive_int, default=100, help="每页数量，默认 100")
    option("max-pages", type=positive_int, default=10000, help="单个接口最多请求页数，默认 10000")
    option("vm-path", default=VM_PATH, help="云主机接口路径，可带固定查询参数")
    option("pod-group-path", default=POD_GROUP_PATH, help="工作负载接口路径，可带固定查询参数")
    option("system-id", type=positive_int, default=1, help="新增服务的业务系统 ID，默认 1")
    option("user-id", type=positive_int, default=1, help="新增服务的用户 ID，默认 1")
    option("team-id", type=positive_int, default=1, help="新增服务的团队 ID，默认 1")
    option("log-path", default=str(Path(__file__).resolve().with_name("sync-biz-service.log")),
           help="日志文件，默认脚本同目录 sync-biz-service.log")
    option("log-debug", action="store_true", help="输出每条变更详情")
    option("dry-run", action="store_true", help="仅预览变更，不写数据库")
    option("skip-delete", action="store_true", help="本轮只新增和更新，跳过删除")
    args = parser.parse_args(argv)
    if args.db_port > 65535 or not 0 <= args.http_port <= 65535:
        parser.error("端口必须在 1–65535 之间（http-port 允许为 0）")
    try:
        endpoint_url(args, args.vm_path)
        endpoint_url(args, args.pod_group_path)
        request_headers(args.http_header)
    except ValueError as exc:
        parser.error(str(exc))
    return args


def endpoint_url(args: argparse.Namespace, path: str) -> str:
    host = urlsplit("//" + args.http_host)
    try:
        port = host.port
    except ValueError as exc:
        raise ValueError("http-host 端口无效") from exc
    if (not host.hostname or host.username is not None or host.password is not None
            or host.path or host.query or host.fragment):
        raise ValueError("http-host 必须是主机名或主机名:端口，不含协议、路径或用户信息")
    if args.http_port and port:
        raise ValueError("http-host 已包含端口，请勿同时指定 http-port")
    if not path.startswith("/") or "#" in path:
        raise ValueError("接口路径必须以 / 开头，不含协议、主机或片段")
    # A leading // is a literal path here, not a new URL authority. Some
    # gateways route /api and //api differently, so preserve the caller's path.
    endpoint_path, _, query = path.partition("?")
    netloc = args.http_host + (":" + str(args.http_port) if args.http_port else "")
    return urlunsplit((args.http_scheme, netloc, endpoint_path, query, ""))


def request_headers(values: Sequence[str]) -> Dict[str, str]:
    headers = {"accept": "application/json"}
    authorization = os.environ.get("CMDB_AUTHORIZATION", "")
    if authorization:
        headers["authorization"] = authorization
    for value in values:
        key, sep, content = value.partition(":")
        key = key.strip().lower()
        if not sep or not re.fullmatch(r"[!#$%&'*+.^_`|~0-9a-z-]+", key):
            raise ValueError("http-header 格式必须为有效的 KEY:VALUE")
        headers[key] = content.strip()
    if any("\r" in value or "\n" in value for value in headers.values()):
        raise ValueError("请求头值不能包含换行")
    return headers


def init_logging(path: str, debug: bool) -> None:
    handlers = [logging.FileHandler(path, encoding="utf-8"), logging.StreamHandler()]
    logging.basicConfig(
        level=logging.DEBUG if debug else logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s", handlers=handlers,
    )


def response_body(response: Any) -> bytes:
    data = response.read()
    headers = getattr(response, "headers", None) or {}
    if headers.get("Content-Encoding", "").strip().lower() in ("gzip", "x-gzip"):
        data = gzip.decompress(data)
    return data


class CMDBClient:
    def __init__(self, headers: Mapping[str, str], timeout: float, page_size: int,
                 max_pages: int = 10000):
        self.headers = dict(headers)
        self.timeout = timeout
        self.page_size = page_size
        self.max_pages = max_pages

    def fetch(self, endpoint: str, label: str) -> List[Dict[str, Any]]:
        parts = urlsplit(endpoint)
        fixed_query = [(key, value) for key, value in parse_qsl(parts.query, keep_blank_values=True)
                       if key not in ("pageSize", "pageNum")]
        items: List[Dict[str, Any]] = []
        seen_pages = set()
        for page in range(1, self.max_pages + 1):
            query = urlencode(fixed_query + [("pageNum", page), ("pageSize", self.page_size)])
            url = urlunsplit((parts.scheme, parts.netloc, parts.path, query, ""))
            request = Request(url, headers=self.headers, method="GET")
            LOG.info("HTTP 请求: %s %s，headers=%s，timeout=%ss",
                     request.method, request.full_url, dict(request.header_items()), self.timeout)
            try:
                with urlopen(request, timeout=self.timeout) as response:
                    payload = json.loads(response_body(response))
            except HTTPError as exc:
                try:
                    body = response_body(exc).decode("utf-8", errors="replace")
                except (OSError, EOFError) as body_error:
                    body = "读取响应正文失败: " + str(body_error)
                finally:
                    exc.close()
                LOG.error("HTTP 响应: status=%d，url=%s，headers=%s，body=%s",
                          exc.code, exc.geturl(), dict(exc.headers or {}), body)
                raise SyncError(f"{label}第 {page} 页 HTTP 状态码 {exc.code}") from exc
            except (URLError, OSError, ValueError) as exc:
                raise SyncError(f"{label}第 {page} 页请求或 JSON 解析失败（{type(exc).__name__}）") from exc
            if not isinstance(payload, dict) or not isinstance(payload.get("list"), list):
                raise SyncError(f"{label}第 {page} 页缺少顶层 list 数组，停止同步")
            rows = payload["list"]
            if any(not isinstance(row, dict) for row in rows):
                raise SyncError(f"{label}第 {page} 页含非对象记录，停止同步")
            LOG.debug("%s第 %d 页: %d 条", label, page, len(rows))
            if not rows:
                LOG.info("获取 %s 数据 %d 条", label, len(items))
                return items
            fingerprint = hashlib.sha256(json.dumps(rows, sort_keys=True).encode("utf-8")).digest()
            if fingerprint in seen_pages:
                raise SyncError(f"{label}第 {page} 页重复，接口可能未正确分页，停止同步")
            seen_pages.add(fingerprint)
            items.extend(rows)
        raise SyncError(f"{label}超过最大页数 {self.max_pages}，停止同步，请检查分页或调整 --max-pages")


def csv_names(value: Optional[str]) -> Tuple[str, ...]:
    return tuple(sorted({part for part in (value or "").split(",") if part}))


def csv_ints(value: Optional[str]) -> Tuple[int, ...]:
    return tuple(sorted({int(part) for part in (value or "").split(",") if part.strip()}))


def join_ints(values: Iterable[int]) -> str:
    return ",".join(str(value) for value in sorted(set(values)))


@dataclass(frozen=True)
class BizService:
    name: str
    type: int
    resources: Tuple[str, ...]
    epc_ids: Tuple[int, ...] = ()
    pod_cluster_ids: Tuple[int, ...] = ()
    pod_namespace_ids: Tuple[int, ...] = ()
    id: Optional[int] = None

    @property
    def key(self) -> ServiceKey:
        return self.type, self.name

    def managed_fields(self) -> Dict[str, str]:
        fields = {"resources": ",".join(self.resources)}
        if self.type == CHOST:
            fields["epc_ids"] = join_ints(self.epc_ids)
        else:
            fields["pod_cluster_ids"] = join_ints(self.pod_cluster_ids)
            fields["pod_namespace_ids"] = join_ints(self.pod_namespace_ids)
        return fields

    @classmethod
    def from_row(cls, row: Mapping[str, Any]) -> BizService:
        return cls(
            id=int(row["id"]), name=row["name"], type=int(row["type"]),
            resources=csv_names(row["resources"]), epc_ids=csv_ints(row["epc_ids"]),
            pod_cluster_ids=csv_ints(row["pod_cluster_ids"]),
            pod_namespace_ids=csv_ints(row["pod_namespace_ids"]),
        )


def resource_index(rows: Sequence[Mapping[str, Any]], label: str) -> Dict[str, Mapping[str, Any]]:
    result: Dict[str, Mapping[str, Any]] = {}
    duplicates = set()
    for row in sorted(rows, key=lambda item: int(item["id"])):
        name = row["name"]
        if name in result:
            duplicates.add(name)
        else:
            result[name] = row
    if duplicates:
        LOG.warning("%s存在 %d 个重名资源，按名称匹配并采用 ID 最小的记录", label, len(duplicates))
        LOG.debug("%s重名资源: %s", label, sorted(duplicates))
    LOG.info("加载 DeepFlow %s %d 条", label, len(rows))
    return result


def build_services(rows: Sequence[Mapping[str, Any]], resources: Mapping[str, Mapping[str, Any]],
                   service_type: int) -> Dict[ServiceKey, BizService]:
    name_field = "hostname" if service_type == CHOST else "name"
    label = "云主机" if service_type == CHOST else "工作负载"
    groups: Dict[str, Dict[str, set]] = {}
    missing_appid = missing_resource = 0
    for row in rows:
        appid = row.get("appid")
        name = row.get(name_field)
        appid = "" if appid is None else appid
        name = "" if name is None else name
        if not isinstance(appid, str) or not isinstance(name, str):
            raise SyncError(f"CMDB {label}的 appid 和 {name_field} 必须是字符串")
        if not appid:
            missing_appid += 1
            continue
        resource = resources.get(name)
        if not name or resource is None:
            missing_resource += 1
            continue
        if "," in name:
            raise SyncError(f"CMDB {label}名称含逗号，无法使用名称列表存储")
        group = groups.setdefault(appid, {"names": set(), "epc": set(), "cluster": set(), "namespace": set()})
        group["names"].add(name)
        if service_type == CHOST:
            group["epc"].add(int(resource["epc_id"] or 0))
        else:
            group["cluster"].add(int(resource["pod_cluster_id"] or 0))
            if resource["pod_namespace_id"]:
                group["namespace"].add(int(resource["pod_namespace_id"]))
    services = {}
    for appid, group in groups.items():
        item = BizService(
            name=appid, type=service_type, resources=tuple(sorted(group["names"])),
            epc_ids=tuple(sorted(group["epc"])), pod_cluster_ids=tuple(sorted(group["cluster"])),
            pod_namespace_ids=tuple(sorted(group["namespace"])),
        )
        services[item.key] = item
    LOG.info("构建 %s 业务服务 %d 条；无 appid %d 条，无 DeepFlow 对应资源 %d 条",
             label, len(services), missing_appid, missing_resource)
    return services


@dataclass
class SyncPlan:
    adds: List[BizService] = field(default_factory=list)
    updates: List[Tuple[BizService, BizService]] = field(default_factory=list)
    deletes: List[BizService] = field(default_factory=list)
    unchanged: int = 0
    retained: int = 0


def make_plan(existing: Sequence[BizService], desired: Mapping[ServiceKey, BizService],
              vm_names: Iterable[str], pod_group_names: Iterable[str], skip_delete: bool = False) -> SyncPlan:
    old: Dict[ServiceKey, BizService] = {}
    for item in existing:
        if item.key in old:
            raise SyncError(f"现有业务服务存在重复的 (type, name): {item.key}，请先明确重复记录归属")
        old[item.key] = item
    plan = SyncPlan()
    for key, new in sorted(desired.items()):
        previous = old.get(key)
        if previous is None:
            plan.adds.append(new)
        elif previous.managed_fields() != new.managed_fields():
            plan.updates.append((previous, new))
        else:
            plan.unchanged += 1
    available = {CHOST: set(vm_names), POD_GROUP: set(pod_group_names)}
    for key, item in sorted(old.items()):
        if key in desired:
            continue
        # Preserve the Go rule: delete only if the app is absent AND all old
        # resource names are absent from the corresponding DeepFlow inventory.
        if (not skip_delete and item.type in available
                and not available[item.type].intersection(item.resources)):
            plan.deletes.append(item)
        else:
            plan.retained += 1
    return plan


def log_plan(plan: SyncPlan, dry_run: bool) -> None:
    LOG.info("%s: 新增 %d，更新 %d，删除 %d，无变化 %d，保留旧服务 %d",
             "预览" if dry_run else "同步计划", len(plan.adds), len(plan.updates),
             len(plan.deletes), plan.unchanged, plan.retained)
    for item in plan.adds:
        LOG.debug("新增 type=%d name=%s: %s", item.type, item.name, item.managed_fields())
    for previous, new in plan.updates:
        LOG.debug("更新 id=%s type=%d name=%s: %s -> %s", previous.id, new.type,
                  new.name, previous.managed_fields(), new.managed_fields())
    for item in plan.deletes:
        LOG.debug("删除 id=%s type=%d name=%s", item.id, item.type, item.name)


class Repository:
    def __init__(self, connection: Any):
        self.connection = connection

    def load(self) -> Tuple[List[BizService], Dict[str, Mapping[str, Any]], Dict[str, Mapping[str, Any]]]:
        with self.connection.cursor() as cursor:
            cursor.execute(
                "SELECT id, name, type, resources, epc_ids, pod_cluster_ids, pod_namespace_ids "
                "FROM biz_service WHERE type IN (%s, %s) AND match_type = %s ORDER BY id",
                (CHOST, POD_GROUP, MATCH_NAME),
            )
            services = [BizService.from_row(row) for row in cursor.fetchall()]
            cursor.execute("SELECT id, name, epc_id FROM vm WHERE deleted_at IS NULL ORDER BY id")
            vms = resource_index(cursor.fetchall(), "云主机")
            cursor.execute(
                "SELECT id, name, pod_cluster_id, pod_namespace_id FROM pod_group "
                "WHERE deleted_at IS NULL ORDER BY id"
            )
            pod_groups = resource_index(cursor.fetchall(), "工作负载")
        LOG.info("加载现有业务服务 %d 条（type=3/5, match_type=1）", len(services))
        return services, vms, pod_groups

    def apply(self, plan: SyncPlan, args: argparse.Namespace) -> None:
        columns = ("lcuuid", "name", "type", "match_type", "epc_ids", "pod_cluster_ids",
                   "pod_namespace_ids", "resources", "resources_context", "team_id", "user_id",
                   "service_group_name", "system_id", "tags")
        insert_sql = (
            "INSERT INTO biz_service (" + ", ".join(columns) + ", created_at, updated_at) VALUES ("
            + ", ".join(["%s"] * len(columns)) + ", CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)"
        )
        with self.connection.cursor() as cursor:
            for previous, new in plan.updates:
                values = new.managed_fields()
                sql = ("UPDATE biz_service SET " + ", ".join(key + " = %s" for key in values)
                       + ", updated_at = CURRENT_TIMESTAMP WHERE id = %s AND type = %s AND match_type = %s")
                cursor.execute(sql, tuple(values.values()) + (previous.id, previous.type, MATCH_NAME))
                if cursor.rowcount != 1:
                    raise SyncError(f"更新业务服务 id={previous.id} 未匹配唯一记录，停止同步")
            for start in range(0, len(plan.adds), 1000):
                records = [
                    (str(uuid.uuid4()), item.name, item.type, MATCH_NAME, join_ints(item.epc_ids),
                     join_ints(item.pod_cluster_ids), join_ints(item.pod_namespace_ids),
                     ",".join(item.resources), "{}", args.team_id, args.user_id, "", args.system_id, "{}")
                    for item in plan.adds[start:start + 1000]
                ]
                cursor.executemany(insert_sql, records)
            for start in range(0, len(plan.deletes), 1000):
                ids = [item.id for item in plan.deletes[start:start + 1000]]
                cursor.execute(
                    "DELETE FROM biz_service WHERE id IN (" + ", ".join(["%s"] * len(ids))
                    + ") AND type IN (%s, %s) AND match_type = %s",
                    tuple(ids) + (CHOST, POD_GROUP, MATCH_NAME),
                )
                if cursor.rowcount != len(ids):
                    raise SyncError("待删除记录发生变化，停止同步")


def connect_database(args: argparse.Namespace) -> Any:
    try:
        import pymysql
    except ImportError as exc:
        raise SyncError("缺少依赖，请执行: python3 -m pip install 'PyMySQL[rsa]>=1.1,<2'") from exc
    return pymysql.connect(
        host=args.db_host, port=args.db_port, user=args.db_user_name, password=args.db_user_pswd,
        database=args.database, charset="utf8mb4", autocommit=False,
        cursorclass=pymysql.cursors.DictCursor, connect_timeout=args.db_timeout,
        read_timeout=args.db_timeout, write_timeout=args.db_timeout,
    )


def sync_database(connection: Any, args: argparse.Namespace,
                  cmdb_vms: Sequence[Mapping[str, Any]], cmdb_pod_groups: Sequence[Mapping[str, Any]]) -> SyncPlan:
    try:
        connection.begin()
        repository = Repository(connection)
        existing, vms, pod_groups = repository.load()
        desired = build_services(cmdb_vms, vms, CHOST)
        desired.update(build_services(cmdb_pod_groups, pod_groups, POD_GROUP))
        plan = make_plan(existing, desired, vms, pod_groups, args.skip_delete)
        log_plan(plan, args.dry_run)
        if args.dry_run:
            connection.rollback()
            LOG.info("预览完成，未写入数据库；--log-debug 可查看每条变更")
        else:
            repository.apply(plan, args)
            connection.commit()
            LOG.info("同步数据完成，事务已提交")
        return plan
    except BaseException:
        connection.rollback()
        raise


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)
    try:
        init_logging(args.log_path, args.log_debug)
    except OSError:
        print("初始化日志失败，请检查 --log-path 的目录和写入权限", file=sys.stderr)
        return 1
    connection = None
    try:
        LOG.info("命令执行参数: %s", vars(args))
        LOG.info("开始%s，数据库=%s，删除=%s", "预览" if args.dry_run else "同步",
                 args.database, "跳过" if args.skip_delete else "启用")
        client = CMDBClient(request_headers(args.http_header), args.http_timeout,
                            args.page_size, args.max_pages)
        # Finish both HTTP reads before opening a database transaction.
        cmdb_vms = client.fetch(endpoint_url(args, args.vm_path), "CMDB 云主机")
        cmdb_pod_groups = client.fetch(endpoint_url(args, args.pod_group_path), "CMDB 工作负载")
        connection = connect_database(args)
        sync_database(connection, args, cmdb_vms, cmdb_pod_groups)
        return 0
    except KeyboardInterrupt:
        LOG.error("同步已中断")
        return 130
    except Exception as exc:
        LOG.error("同步失败: %s", exc)
        return 1
    finally:
        if connection is not None:
            connection.close()


if __name__ == "__main__":
    sys.exit(main())

"""Offline checks: CMDB responses are mocked; SQL runs against local SQLite.

The SQLite adapter checks SQL/data flow and rollback, not MySQL-specific behavior.
Run: python3 -m unittest discover -s tests -v
"""

import contextlib
import gzip
import io
import json
import os
import sqlite3
import sys
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit

# Direct execution puts tests/ on sys.path rather than the project directory.
if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import sync_biz_service as sync


class SQLiteCursor:
    def __init__(self, connection):
        self.cursor = connection.cursor()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.cursor.close()

    def execute(self, sql, params=()):
        self.cursor.execute(sql.replace("%s", "?"), params)

    def executemany(self, sql, records):
        self.cursor.executemany(sql.replace("%s", "?"), records)

    def fetchall(self):
        return [dict(row) for row in self.cursor.fetchall()]

    @property
    def rowcount(self):
        return self.cursor.rowcount


class SQLiteConnection:
    def __init__(self):
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        self.commits = 0
        self.rollbacks = 0
        self.db.executescript("""
            CREATE TABLE biz_service (
                id INTEGER PRIMARY KEY, lcuuid TEXT UNIQUE, name TEXT NOT NULL,
                type INTEGER, match_type INTEGER, epc_ids TEXT DEFAULT '',
                pod_cluster_ids TEXT DEFAULT '', pod_namespace_ids TEXT DEFAULT '',
                resources TEXT DEFAULT '', resources_context TEXT DEFAULT '{}',
                team_id INTEGER, user_id INTEGER, system_id INTEGER,
                service_group_name TEXT DEFAULT '', tags TEXT DEFAULT '{}',
                created_at TEXT, updated_at TEXT
            );
            CREATE TABLE vm (id INTEGER PRIMARY KEY, name TEXT, epc_id INTEGER, deleted_at TEXT);
            CREATE TABLE pod_group (
                id INTEGER PRIMARY KEY, name TEXT, pod_cluster_id INTEGER,
                pod_namespace_id INTEGER, deleted_at TEXT
            );
        """)

    def cursor(self):
        return SQLiteCursor(self.db)

    def begin(self):
        self.db.execute("BEGIN")

    def commit(self):
        self.db.commit()
        self.commits += 1

    def rollback(self):
        self.db.rollback()
        self.rollbacks += 1

    def close(self):
        self.db.close()


class CMDBTests(unittest.TestCase):
    def client(self, **kwargs):
        return sync.CMDBClient({"Authorization": "test-token"}, 3, 2, **kwargs)

    def responses(self, payloads):
        return [io.BytesIO(json.dumps(payload).encode()) for payload in payloads]

    def test_pagination_rebuilds_url_and_preserves_fixed_query(self):
        pages = self.responses([
            {"list": [{"appid": "app-a", "hostname": "vm-a"}]},
            {"list": [{"appid": "app-b", "hostname": "vm-b"}]}, {"list": []},
        ])
        with patch.object(sync, "urlopen", side_effect=pages) as opener:
            rows = self.client().fetch("http://cmdb/hosts?cluster=7&pageNum=99&pageSize=9", "VM")
        self.assertEqual(2, len(rows))
        for page, call in enumerate(opener.call_args_list, 1):
            request = call[0][0]
            self.assertEqual({"cluster": ["7"], "pageSize": ["2"], "pageNum": [str(page)]},
                             parse_qs(urlsplit(request.full_url).query))
            self.assertEqual("test-token", request.get_header("Authorization"))
            self.assertEqual(3, call[1]["timeout"])

    def test_missing_list_or_wrong_shape_is_an_error(self):
        for payload in ({"error": "denied"}, {"list": None}, {"list": {}}, [], {"list": [1]}):
            with self.subTest(payload=payload), patch.object(
                sync, "urlopen", side_effect=self.responses([payload])
            ), self.assertRaises(sync.SyncError):
                self.client().fetch("http://cmdb/hosts", "VM")

    def test_repeated_page_aborts_instead_of_looping(self):
        page = {"list": [{"appid": "a", "hostname": "vm"}]}
        with patch.object(sync, "urlopen", side_effect=self.responses([page, page])), \
                self.assertRaisesRegex(sync.SyncError, "重复"):
            self.client().fetch("http://cmdb/hosts", "VM")

    def test_max_pages_aborts_instead_of_using_partial_data(self):
        with patch.object(sync, "urlopen", side_effect=self.responses([{"list": [{}]}])), \
                self.assertRaisesRegex(sync.SyncError, "最大页数"):
            self.client(max_pages=1).fetch("http://cmdb/hosts", "VM")

    def test_double_slash_gateway_path_is_preserved_on_every_page(self):
        args = sync.parse_args(["--http-host", "cmdb", "--vm-path", "//api/v1/hosts", "--page-size", "5"])
        endpoint = sync.endpoint_url(args, args.vm_path)
        client = sync.CMDBClient({"Authorization": "test-token"}, 3, args.page_size)
        with patch.object(sync, "urlopen", side_effect=self.responses([
            {"list": [{"appid": "a", "hostname": "vm"}]}, {"list": []},
        ])) as opener:
            client.fetch(endpoint, "VM")
        self.assertEqual("http://cmdb//api/v1/hosts?pageNum=1&pageSize=5", opener.call_args_list[0][0][0].full_url)
        self.assertEqual("http://cmdb//api/v1/hosts?pageNum=2&pageSize=5", opener.call_args_list[1][0][0].full_url)

    def test_gzip_response_is_decoded_before_json_parsing(self):
        first = io.BytesIO(gzip.compress(json.dumps({"list": [{"appid": "a"}]}).encode()))
        first.headers = {"Content-Encoding": "gzip"}
        with patch.object(sync, "urlopen", side_effect=[first, io.BytesIO(b'{"list": []}')]):
            rows = self.client().fetch("http://cmdb/hosts", "VM")
        self.assertEqual([{"appid": "a"}], rows)

    def test_http_error_logs_response_body_headers_and_final_url(self):
        error = HTTPError("http://cmdb/final", 401, "Unauthorized", {"Server": "gateway"},
                          io.BytesIO(b'{"message":"authentication rejected by gateway"}'))
        with patch.object(sync, "urlopen", side_effect=error), self.assertLogs(sync.LOG, "ERROR") as logs:
            with self.assertRaisesRegex(sync.SyncError, "401"):
                self.client().fetch("http://cmdb/hosts", "VM")
        self.assertIn("authentication rejected by gateway", logs.output[0])
        self.assertIn("http://cmdb/final", logs.output[0])
        self.assertIn("gateway", logs.output[0])

    def test_http_and_json_errors_do_not_expose_body_or_headers(self):
        errors = [HTTPError("http://cmdb", 401, "bad", {}, None), URLError("unavailable")]
        for error in errors:
            with patch.object(sync, "urlopen", side_effect=error), \
                    patch.object(sync.LOG, "error"), \
                    self.assertRaises(sync.SyncError) as caught:
                self.client().fetch("http://cmdb/hosts", "VM")
            self.assertNotIn("test-token", str(caught.exception))
        with patch.object(sync, "urlopen", return_value=io.BytesIO(b"secret-response")), \
                self.assertRaises(sync.SyncError) as caught:
            self.client().fetch("http://cmdb/hosts", "VM")
        self.assertNotIn("secret-response", str(caught.exception))


class PlanningTests(unittest.TestCase):
    def test_aggregation_deduplicates_and_sorts_names_and_scopes(self):
        rows = [{"appid": "app", "hostname": name} for name in ("z", "a", "a")]
        rows += [{"appid": "", "hostname": "a"}, {"appid": "other", "hostname": "missing"}]
        services = sync.build_services(rows, {"z": {"epc_id": 9}, "a": {"epc_id": 2}}, sync.CHOST)
        self.assertEqual({(sync.CHOST, "app")}, set(services))
        self.assertEqual(("a", "z"), services[(sync.CHOST, "app")].resources)
        self.assertEqual((2, 9), services[(sync.CHOST, "app")].epc_ids)

    def test_pod_aggregation_keeps_cluster_and_skips_zero_namespace(self):
        rows = [{"appid": "app", "name": name} for name in ("a", "b")]
        services = sync.build_services(rows, {
            "a": {"pod_cluster_id": 3, "pod_namespace_id": 0},
            "b": {"pod_cluster_id": 7, "pod_namespace_id": 5},
        }, sync.POD_GROUP)
        item = services[(sync.POD_GROUP, "app")]
        self.assertEqual((3, 7), item.pod_cluster_ids)
        self.assertEqual((5,), item.pod_namespace_ids)

    def test_non_string_cmdb_fields_abort_instead_of_becoming_empty_data(self):
        for row in ({"appid": 0, "hostname": "vm"}, {"appid": "app", "hostname": 123}):
            with self.subTest(row=row), self.assertRaises(sync.SyncError):
                sync.build_services([row], {"vm": {"epc_id": 1}}, sync.CHOST)

    def test_resource_names_with_spaces_round_trip_without_permanent_changes(self):
        desired = sync.build_services([{"appid": "app", "hostname": " vm "}],
                                      {" vm ": {"epc_id": 1}}, sync.CHOST)
        row = {"id": 1, "name": "app", "type": 3, "resources": " vm ", "epc_ids": "1",
               "pod_cluster_ids": "", "pod_namespace_ids": ""}
        plan = sync.make_plan([sync.BizService.from_row(row)], desired, [" vm "], [])
        self.assertEqual(1, plan.unchanged)
        self.assertEqual([], plan.updates)

    def test_same_appid_in_both_types_remains_separate(self):
        old = [sync.BizService("app", sync.CHOST, ("vm",), epc_ids=(1,), id=1)]
        new = sync.BizService("app", sync.POD_GROUP, ("workload",), pod_cluster_ids=(2,))
        plan = sync.make_plan(old, {new.key: new}, ["vm"], ["workload"])
        self.assertEqual([new], plan.adds)
        self.assertEqual([], plan.updates)
        self.assertEqual([], plan.deletes)

    def test_scope_only_change_still_updates(self):
        for old, new in (
            (sync.BizService("a", sync.CHOST, ("vm",), epc_ids=(1,), id=1),
             sync.BizService("a", sync.CHOST, ("vm",), epc_ids=(2,))),
            (sync.BizService("a", sync.POD_GROUP, ("pg",), pod_cluster_ids=(1,), id=2),
             sync.BizService("a", sync.POD_GROUP, ("pg",), pod_cluster_ids=(2,), pod_namespace_ids=(3,))),
        ):
            with self.subTest(type=old.type):
                plan = sync.make_plan([old], {new.key: new}, ["vm"], ["pg"])
                self.assertEqual([(old, new)], plan.updates)

    def test_deletion_requires_absent_app_and_absent_resources_of_same_type(self):
        old = [sync.BizService("removed", sync.CHOST, ("gone",), id=1),
               sync.BizService("retained", sync.CHOST, ("gone", "live"), id=2),
               sync.BizService("pod", sync.POD_GROUP, ("gone",), id=3)]
        plan = sync.make_plan(old, {}, ["live"], ["gone"])
        self.assertEqual([old[0]], plan.deletes)
        self.assertEqual(2, plan.retained)
        self.assertEqual([], sync.make_plan(old, {}, [], [], skip_delete=True).deletes)

    def test_duplicate_existing_services_abort(self):
        old = [sync.BizService("app", sync.CHOST, (), id=id_) for id_ in (1, 2)]
        with self.assertRaisesRegex(sync.SyncError, "重复"):
            sync.make_plan(old, {}, [], [])

    def test_duplicate_resource_names_use_lowest_id(self):
        rows = [{"id": 9, "name": "vm", "epc_id": 90}, {"id": 2, "name": "vm", "epc_id": 20}]
        with self.assertLogs(sync.LOG, "WARNING"):
            result = sync.resource_index(rows, "VM")
        self.assertEqual(20, result["vm"]["epc_id"])


class DatabaseTests(unittest.TestCase):
    def setUp(self):
        self.connection = SQLiteConnection()
        self.addCleanup(self.connection.close)
        self.args = sync.parse_args([])

    def prepare(self):
        self.connection.db.commit()

    def services(self):
        return [dict(row) for row in self.connection.db.execute("SELECT * FROM biz_service ORDER BY id")]

    def test_sql_round_trip_both_types_then_idempotent(self):
        self.connection.db.execute("INSERT INTO vm VALUES (1, 'vm', 7, NULL)")
        self.connection.db.execute("INSERT INTO pod_group VALUES (1, 'pg', 8, 9, NULL)")
        self.prepare()
        vm = [{"appid": "same-app", "hostname": "vm"}]
        pg = [{"appid": "same-app", "name": "pg"}]
        first = sync.sync_database(self.connection, self.args, vm, pg)
        rows = self.services()
        self.assertEqual(2, len(first.adds))
        self.assertEqual([3, 5], [row["type"] for row in rows])
        self.assertEqual("7", rows[0]["epc_ids"])
        self.assertEqual("8", rows[1]["pod_cluster_ids"])
        self.assertEqual("9", rows[1]["pod_namespace_ids"])
        self.assertEqual("{}", rows[0]["tags"])
        second = sync.sync_database(self.connection, self.args, vm, pg)
        self.assertEqual([], second.adds + second.updates + second.deletes)
        self.assertEqual(2, second.unchanged)
        self.assertEqual(rows, self.services())

    def test_update_preserves_ownership_tags_context_and_lcuuid(self):
        self.connection.db.execute("INSERT INTO vm VALUES (1, 'vm', 9, NULL)")
        self.connection.db.execute("""
            INSERT INTO biz_service (id, lcuuid, name, type, match_type, resources, epc_ids,
                team_id, user_id, system_id, tags, resources_context, service_group_name)
            VALUES (1, 'original', 'app', 3, 1, 'vm', '1', 7, 8, 9, '{"k":"v"}', '{"x":1}', 'group')
        """)
        self.prepare()
        sync.sync_database(self.connection, self.args, [{"appid": "app", "hostname": "vm"}], [])
        row = self.services()[0]
        self.assertEqual("9", row["epc_ids"])
        self.assertEqual(("original", 7, 8, 9, '{"k":"v"}', '{"x":1}', "group"),
                         tuple(row[key] for key in ("lcuuid", "team_id", "user_id", "system_id",
                                                   "tags", "resources_context", "service_group_name")))

    def test_filters_soft_deleted_resources_and_unmanaged_services(self):
        self.connection.db.execute("INSERT INTO vm VALUES (1, 'vm', 9, '2026-01-01')")
        self.connection.db.execute("INSERT INTO pod_group VALUES (1, 'pg', 1, 2, '2026-01-01')")
        self.connection.db.executemany(
            "INSERT INTO biz_service (id, name, type, match_type, resources) VALUES (?, ?, ?, ?, ?)",
            [(1, "old", 3, 1, "vm"), (2, "manual", 1, 1, "gone"), (3, "uid", 3, 2, "gone")],
        )
        self.prepare()
        plan = sync.sync_database(self.connection, self.args,
                                  [{"appid": "new", "hostname": "vm"}], [{"appid": "new", "name": "pg"}])
        self.assertEqual([], plan.adds)
        self.assertEqual([1], [item.id for item in plan.deletes])
        self.assertEqual([2, 3], [row["id"] for row in self.services()])

    def test_dry_run_performs_no_writes(self):
        self.connection.db.execute("INSERT INTO vm VALUES (1, 'vm', 1, NULL)")
        self.connection.db.execute("INSERT INTO biz_service (id,name,type,match_type) VALUES (1,'old',3,1)")
        self.prepare()
        before = self.services()
        self.args.dry_run = True
        plan = sync.sync_database(self.connection, self.args, [{"appid": "app", "hostname": "vm"}], [])
        self.assertEqual(1, len(plan.adds))
        self.assertEqual(1, len(plan.deletes))
        self.assertEqual(before, self.services())
        self.assertEqual(0, self.connection.commits)
        self.assertEqual(1, self.connection.rollbacks)

    def test_late_insert_failure_rolls_back_prior_update(self):
        self.connection.db.execute("INSERT INTO vm VALUES (1, 'vm', 9, NULL)")
        self.connection.db.execute("INSERT INTO biz_service (id,name,type,match_type,resources,epc_ids) "
                                   "VALUES (1,'app',3,1,'vm','1')")
        self.connection.db.executescript("""
            CREATE TRIGGER fail_insert BEFORE INSERT ON biz_service
            WHEN NEW.name = 'fail' BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END;
        """)
        self.prepare()
        before = self.services()
        with self.assertRaisesRegex(sqlite3.IntegrityError, "simulated"):
            sync.sync_database(self.connection, self.args,
                               [{"appid": "app", "hostname": "vm"}, {"appid": "fail", "hostname": "vm"}], [])
        self.assertEqual(before, self.services())
        self.assertEqual(0, self.connection.commits)
        self.assertEqual(1, self.connection.rollbacks)

    def test_batch_insert_and_delete_more_than_1000(self):
        plan = sync.SyncPlan(adds=[sync.BizService(f"app-{i}", sync.CHOST, ("vm",), epc_ids=(1,))
                                  for i in range(1001)])
        self.connection.begin()
        sync.Repository(self.connection).apply(plan, self.args)
        self.connection.commit()
        rows = self.services()
        self.assertEqual(1001, len(rows))
        plan = sync.SyncPlan(deletes=[sync.BizService.from_row(row) for row in rows])
        self.connection.begin()
        sync.Repository(self.connection).apply(plan, self.args)
        self.connection.commit()
        self.assertEqual([], self.services())


class CommandTests(unittest.TestCase):
    def test_old_flags_durations_and_environment_secrets(self):
        with patch.dict(os.environ, {"DEEPFLOW_DB_PASSWORD": "test-db-secret", "CMDB_AUTHORIZATION": "test-auth-secret"}):
            args = sync.parse_args(["-database", "custom", "--http-timeout", "500ms", "--dry-run"])
            self.assertEqual("custom", args.database)
            self.assertEqual("test-db-secret", args.db_user_pswd)
            self.assertEqual(0.5, args.http_timeout)
            self.assertEqual("override", sync.request_headers(["Authorization: override"])["authorization"])
            output = io.StringIO()
            with contextlib.redirect_stdout(output), self.assertRaises(SystemExit):
                sync.parse_args(["--help"])
            self.assertNotIn("test-db-secret", output.getvalue())
            self.assertNotIn("test-auth-secret", output.getvalue())

    def test_invalid_configuration_is_rejected_before_io(self):
        for flags in (["--page-size", "0"], ["--http-timeout", "0s"], ["--db-port", "65536"],
                      ["--http-port", "-1"], ["--http-host", "http://cmdb"],
                      ["--vm-path", "https://other/hosts"], ["--http-header", "Authorization: x\ny"]):
            with self.subTest(flags=flags), contextlib.redirect_stderr(io.StringIO()), \
                    self.assertRaises(SystemExit):
                sync.parse_args(flags)

    def test_api_failure_prevents_database_connection(self):
        with patch.object(sync, "init_logging"), patch.object(sync, "connect_database") as connect, \
                patch.object(sync.CMDBClient, "fetch", side_effect=[[], sync.SyncError("bad response")]), \
                self.assertLogs(sync.LOG, "ERROR"):
            self.assertEqual(1, sync.main(["--dry-run"]))
        connect.assert_not_called()

    def test_connection_options_and_no_autocommit(self):
        from types import SimpleNamespace
        driver = SimpleNamespace(connect=unittest.mock.Mock(), cursors=SimpleNamespace(DictCursor=object()))
        with patch.dict(sys.modules, {"pymysql": driver}):
            sync.connect_database(sync.parse_args([]))
        kwargs = driver.connect.call_args[1]
        self.assertFalse(kwargs["autocommit"])
        self.assertEqual("utf8mb4", kwargs["charset"])
        self.assertIs(driver.cursors.DictCursor, kwargs["cursorclass"])

if __name__ == "__main__":
    unittest.main()

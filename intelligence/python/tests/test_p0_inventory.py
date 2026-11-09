"""P0/P1 consistency checks: benchmark inventory and the Phase 1 schema (migration 0001)."""
import pathlib
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[3]


def test_benchmark_inventory_has_rs1_to_rs12():
    names = {p.stem for p in (ROOT / "benchmarks" / "scenarios").glob("RS-*.yaml")}
    assert names == {f"RS-{i}" for i in range(1, 13)}
    for i in range(1, 13):
        text = (ROOT / "benchmarks" / "scenarios" / f"RS-{i}.yaml").read_text()
        assert text.startswith(f"id: RS-{i}\n")


def _data_model_sql():
    sql = (ROOT / "core" / "go" / "internal" / "store" / "migrations" / "0001_init.sql").read_text()
    return "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT, checksum TEXT, applied_at INTEGER);" + sql


def test_data_model_sql_creates_only_phase1_tables():
    db = sqlite3.connect(":memory:")
    db.executescript(_data_model_sql())
    tables = {r[0] for r in db.execute("select name from sqlite_master where type='table'")}
    assert tables == {
        "schema_migrations", "tasks", "task_steps", "task_events", "checkpoints", "capability_grants",
        "approvals", "providers", "model_tiers", "usage_records", "audit_events", "instance",
    }


def test_events_and_audit_are_append_only():
    db = sqlite3.connect(":memory:")
    db.executescript(_data_model_sql())
    db.execute("insert into tasks(id,objective,state,root_task_id,budget_json,config_hash,created_at,updated_at)"
               " values('T','o','CREATED','T','{}','h',0,0)")
    db.execute("insert into task_events(id,task_id,seq,type,actor,data_json,at) values('E','T',1,'X','core','{}',0)")
    db.execute("insert into audit_events(id,type,actor,data_json,at) values('A','X','core','{}',0)")
    for sql in ("update task_events set type='Y'", "delete from task_events",
                "update audit_events set type='Y'", "delete from audit_events"):
        try:
            db.execute(sql)
        except sqlite3.DatabaseError as e:
            assert "append-only" in str(e)
        else:
            raise AssertionError(f"{sql} was allowed")

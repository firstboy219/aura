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
    mig = ROOT / "core" / "go" / "internal" / "store" / "migrations"
    sql = "\n".join(p.read_text() for p in sorted(mig.glob("*.sql")))
    return "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT, checksum TEXT, applied_at INTEGER);" + sql


def test_migrations_create_exactly_the_planned_tables():
    db = sqlite3.connect(":memory:")
    db.executescript(_data_model_sql())
    tables = {r[0] for r in db.execute("select name from sqlite_master where type='table'")
              if not r[0].endswith(("_fts", "_data", "_idx", "_content", "_docsize", "_config"))}
    assert tables == {
        "schema_migrations", "tasks", "task_steps", "task_events", "checkpoints", "capability_grants",
        "approvals", "providers", "model_tiers", "usage_records", "audit_events", "instance",
        # P2 (migration 0002)
        "task_waits", "task_inputs", "shared_context", "external_events",
        # P3 (migration 0003)
        "memories", "skills", "skill_versions", "profile_items", "routing_shadow", "triggers", "watch_state", "notifications",
        "limitations", "limitation_remedies",  # Limitation Ledger (Blueprint 8A.5)
        # P4 (migration 0005, ADR-0036)
        "secrets",
        "l2_tools",  # migration 0006, ADR-0038
        "web_owner", "web_sessions", "web_setup",  # migration 0007, ADR-0041
        "members", "settings",  # migration 0008, ADR-0043
        "channel_deliveries",  # migration 0009, ADR-0044
        "brain_variants",  # migration 0010, ADR-0048
        "lab_runs",  # migration 0010, ADR-0048
        "aul_workflows", "aul_runs",  # migration 0012, ADR-0050
        "app_accounts",  # migration 0013, ADR-0053
        "task_replies",  # migration 0014, ADR-0055
        "ui_screens",  # migration 0015, ADR-0060
        "notes", "media", "media_lookups",  # migration 0016, ADR-0061
        "reminders",  # migration 0017, ADR-0063
        "shell_log",  # migration 0018, ADR-0065
        # ODI (Blueprint v1.1): additive migrations 0019 (columns only), 0022 (columns + a trigger), and the tables below
        "fallback_authorizations", "teacher_requests", "nexus_shadow",  # migration 0020, ADR-0073
        "devices", "device_pairings", "device_nonces", "device_leases",  # migration 0021, ADR-0075
        "change_proposals",  # migration 0023, ADR-0077
        "teaching_examples",  # migration 0024, ADR-0078
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

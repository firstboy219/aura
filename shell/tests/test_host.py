"""Tests for the shell host (ADR-0065). Run in the aura-shell image: deploy/shell-test.sh."""

import base64
import os
import tempfile

import pytest

os.environ["AURA_SHELL_WORK"] = tempfile.mkdtemp()
from aura_shell import host

H = host.Host()


def run(cmd, **kw):
    return H.exec({"task_id": "T1", "command": cmd, "run_id": "r", **kw})


def test_runs_in_its_own_task_directory_with_a_clean_environment():
    r = run("pwd; echo $HOME; env | sort | cut -d= -f1 | tr '\\n' ' '")
    assert r["exit_code"] == 0
    lines = r["stdout"].splitlines()
    assert lines[0].endswith("/T1") and lines[1].endswith("/T1")
    assert "AURA" not in r["stdout"]  # nothing of the host's environment leaks in


def test_exit_code_stderr_and_output_cap():
    r = run("echo oops >&2; exit 3")
    assert r["exit_code"] == 3 and "oops" in r["stderr"]
    r = run("head -c 200000 /dev/zero | tr '\\0' a")
    assert r["stdout_bytes"] == 200000 and len(r["stdout"]) == host.MAX_OUTPUT


def test_timeout_kills_the_whole_process_group():
    r = run("sleep 30 & sleep 30; echo never", timeout_s=1)
    assert r["timed_out"] and "never" not in r["stdout"] and r["duration_ms"] < 10000


def test_files_move_in_and_out_and_paths_stay_inside():
    r = run("tr a-z A-Z < in/x.txt > out.txt", files_in=[{"path": "in/x.txt", "content_base64": base64.b64encode(b"halo").decode()}],
            files_out=["out.txt", "nope.txt"])
    assert base64.b64decode(r["files"][0]["content_base64"]) == b"HALO"
    assert r["files"][1]["missing"]
    for bad in ("../x", "/etc/passwd", "a/../../x"):
        with pytest.raises(host.HostError):
            run("true", files_out=[bad])
    os.symlink("/etc", os.path.join(host.WORK, "T1", "link"))
    with pytest.raises(host.HostError):
        run("true", files_out=["link/passwd"])


def test_bad_task_ids_and_commands_are_refused():
    for bad in ("../x", "", "a/b"):
        with pytest.raises(host.HostError):
            H.exec({"task_id": bad, "command": "true"})
    with pytest.raises(host.HostError):
        run("x" * (host.MAX_COMMAND + 1))
    assert H.handle({"id": "1", "method": "os.system", "params": {}})["error"]["code"] == "unknown_method"


def test_proxy_is_passed_to_the_command():
    r = run("echo $HTTPS_PROXY", proxy="http://s:t@aura-core:7480")
    assert r["stdout"].strip() == "http://s:t@aura-core:7480"


def test_resource_limits_apply():
    r = run("ulimit -u; ulimit -c; ulimit -t")
    assert r["stdout"].split() == ["256", "0", "125"]

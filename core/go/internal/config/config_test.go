package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noenv(string) string { return "" }

func TestDefaultsAreValid(t *testing.T) {
	if _, err := Parse([]byte("config_version: 1\n"), noenv); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeyRefusesStart(t *testing.T) {
	_, err := Parse([]byte("config_version: 1\nserver:\n  bindd: x\n"), noenv)
	if err == nil || !strings.Contains(err.Error(), "bindd") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestInvalidValuesRefuseStart(t *testing.T) {
	cases := []string{
		"config_version: 2\n",
		"worker:\n  max_concurrent_tasks: 2\n",
		"budgets:\n  task_default: {max_cost_usd: 0, max_iterations: 1, max_wall_seconds: 1}\n",
		"models:\n  turbo: []\n",
		"models:\n  fast: [{display_name: a, api_model_id: b, route_id: c}]\n", // no price
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c), noenv); err == nil {
			t.Errorf("accepted invalid config %q", c)
		}
	}
}

func TestEnvOverride(t *testing.T) {
	env := map[string]string{"AURA_SERVER_BIND": "127.0.0.1:9999", "AURA_BUDGETS_GLOBAL_DAILY_COST_USD": "1.5"}
	c, err := Parse([]byte("config_version: 1\n"), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Bind != "127.0.0.1:9999" || c.Budgets.Global.DailyCostUSD != 1.5 {
		t.Fatalf("%+v", c.Server)
	}
}

func TestReadSecretChecks(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("s3cret-value\n"), 0o400)
	if s, err := ReadSecret(good); err != nil || s != "s3cret-value" {
		t.Fatalf("%q %v", s, err)
	}
	wide := filepath.Join(dir, "wide")
	os.WriteFile(wide, []byte("s3cret-value"), 0o644)
	if _, err := ReadSecret(wide); err == nil {
		t.Fatal("accepted 0644 secret")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(good, link)
	if _, err := ReadSecret(link); err == nil {
		t.Fatal("accepted symlink secret")
	}
	_, err := ReadSecret(wide)
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatal("error leaks content")
	}
}

func TestHashIsStable(t *testing.T) {
	a, _ := Parse([]byte("config_version: 1\n"), noenv)
	b, _ := Parse([]byte("config_version: 1\n"), noenv)
	if a.Hash() != b.Hash() {
		t.Fatal("hash not stable")
	}
}

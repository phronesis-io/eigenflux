package sanity

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// projectRoot returns the repository root (two levels up from tests/sanity/).
func projectRoot(t *testing.T) string {
	t.Helper()
	// Walk up from working directory until we find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			t.Fatal("could not find project root (no go.mod)")
		}
		dir = parent
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// parseBuildServices extracts service names from scripts/common/build.sh ALL_SERVICES array.
// Format: "name:./path/"
func parseBuildServices(t *testing.T, content string) []string {
	t.Helper()
	re := regexp.MustCompile(`"(\w+):\./`)
	matches := re.FindAllStringSubmatch(content, -1)
	var names []string
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// parseLocalServices extracts service names from scripts/local/start_local.sh SERVICE_MAP array.
// Format: "name:${PORT}" or "name:"
func parseLocalServices(t *testing.T, content string) []string {
	t.Helper()
	// Find the SERVICE_MAP block.
	start := strings.Index(content, "SERVICE_MAP=(")
	if start == -1 {
		t.Fatal("SERVICE_MAP not found in start_local.sh")
	}
	end := strings.Index(content[start:], ")")
	block := content[start : start+end]

	re := regexp.MustCompile(`"(\w+):`)
	matches := re.FindAllStringSubmatch(block, -1)
	var names []string
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// parseCloudModules extracts module names from scripts/cloud/services.sh ALL_MODULES array.
// Format: ALL_MODULES=(name1 name2 ...)
func parseCloudModules(t *testing.T, content string) []string {
	t.Helper()
	re := regexp.MustCompile(`ALL_MODULES=\(([^)]+)\)`)
	m := re.FindStringSubmatch(content)
	if m == nil {
		t.Fatal("ALL_MODULES not found in services.sh")
	}
	return strings.Fields(m[1])
}

func toSet(names []string) map[string]bool {
	s := make(map[string]bool, len(names))
	for _, n := range names {
		s[n] = true
	}
	return s
}

func sorted(s map[string]bool) []string {
	var out []string
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diff returns elements in a but not in b.
func diff(a, b map[string]bool) map[string]bool {
	d := make(map[string]bool)
	for k := range a {
		if !b[k] {
			d[k] = true
		}
	}
	return d
}

// TestServiceListConsistency verifies that all three service list files
// (build.sh, start_local.sh, services.sh) define the same set of services.
// Cloud services.sh may include "etcd" (infrastructure, not a Go service).
func TestServiceListConsistency(t *testing.T) {
	root := projectRoot(t)

	buildContent := readFile(t, root+"/scripts/common/build.sh")
	localContent := readFile(t, root+"/scripts/local/start_local.sh")
	cloudContent := readFile(t, root+"/scripts/cloud/services.sh")

	buildServices := toSet(parseBuildServices(t, buildContent))
	localServices := toSet(parseLocalServices(t, localContent))
	cloudModules := toSet(parseCloudModules(t, cloudContent))

	// etcd is infrastructure managed by cloud scripts, not a Go service in build/local.
	cloudOnly := map[string]bool{"etcd": true}
	cloudGoServices := make(map[string]bool)
	for k := range cloudModules {
		if !cloudOnly[k] {
			cloudGoServices[k] = true
		}
	}

	// Check: build.sh vs start_local.sh
	if missing := diff(buildServices, localServices); len(missing) > 0 {
		t.Errorf("services in build.sh but missing from start_local.sh SERVICE_MAP: %v", sorted(missing))
	}
	if extra := diff(localServices, buildServices); len(extra) > 0 {
		t.Errorf("services in start_local.sh SERVICE_MAP but missing from build.sh: %v", sorted(extra))
	}

	// Check: build.sh vs cloud services.sh
	if missing := diff(buildServices, cloudGoServices); len(missing) > 0 {
		t.Errorf("services in build.sh but missing from cloud services.sh ALL_MODULES: %v", sorted(missing))
	}
	if extra := diff(cloudGoServices, buildServices); len(extra) > 0 {
		t.Errorf("services in cloud services.sh ALL_MODULES but missing from build.sh: %v", sorted(extra))
	}
}

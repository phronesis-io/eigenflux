package desktopnotify

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func helperVersionFixture(t *testing.T, version string, signed bool) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), appName)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0700); err != nil {
		t.Fatal(err)
	}
	versionField := ""
	if version != "" {
		versionField = "<key>CFBundleVersion</key><string>" + html.EscapeString(version) + "</string>"
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>ai.eigenflux.notifications.test</string><key>CFBundleExecutable</key><string>EigenFluxNotifications</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleShortVersionString</key><string>99.0.0</string>%s</dict></plist>`, versionField)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "EigenFluxNotifications"), data, 0700); err != nil {
		t.Fatal(err)
	}
	if signed {
		if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
			t.Fatalf("sign fixture: %s %v", out, err)
		}
	}
	return app
}

func TestSharedHelperPreservesOnlyVerifiedNewerRelease(t *testing.T) {
	for _, tt := range []struct {
		name, source, destination string
		keep                      bool
	}{
		{"higher installed", "1.0.33", "1.0.34", true},
		{"lower installed", "1.0.33", "1.0.32", false},
		{"same release", "1.0.33", "1.0.33", false},
		{"legacy source", "2", "1.0.34", true},
		{"missing source release", "", "1.0.34", true},
		{"legacy installed counter", "1.0.33", "2", false},
		{"missing installed release", "1.0.33", "", false},
		{"legacy bundles", "2", "2", false},
		{"unstable installed version", "1.0.33", "99.0.0-beta", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := helperVersionFixture(t, tt.source, false)
			destination := helperVersionFixture(t, tt.destination, tt.keep)
			before, err := bundleDigest(destination)
			if err != nil {
				t.Fatal(err)
			}
			got, err := newerInstalledHelper(context.Background(), source, destination)
			if err != nil || got != tt.keep {
				t.Fatalf("keep=%v err=%v want=%v", got, err, tt.keep)
			}
			after, err := bundleDigest(destination)
			if err != nil || after != before {
				t.Fatal("version selection changed the installed bundle")
			}
		})
	}
}

func TestSharedHelperDoesNotTrustUnsignedHigherVersion(t *testing.T) {
	source := helperVersionFixture(t, "1.0.33", false)
	destination := helperVersionFixture(t, "99.0.0", false)
	keep, err := newerInstalledHelper(context.Background(), source, destination)
	if err == nil || keep {
		t.Fatalf("unsigned helper retained: keep=%v err=%v", keep, err)
	}
	if err := verifyHelperSignature(context.Background(), source); err == nil {
		t.Fatal("unsigned source signature accepted")
	}
}

func TestHelperVersionUsesBuildVersionForXMLAndBinaryPlists(t *testing.T) {
	app := helperVersionFixture(t, "1.0.33", false)
	for _, binary := range []bool{false, true} {
		if binary {
			if out, err := exec.Command("/usr/bin/plutil", "-convert", "binary1", filepath.Join(app, "Contents", "Info.plist")).CombinedOutput(); err != nil {
				t.Fatalf("binary fixture: %s %v", out, err)
			}
		}
		got, err := helperBundleVersion(context.Background(), app)
		if err != nil || got != "1.0.33" {
			t.Fatalf("version=%q err=%v", got, err)
		}
	}
	if got, err := helperBundleVersion(context.Background(), filepath.Join(t.TempDir(), "missing.app")); err != nil || got != "" {
		t.Fatalf("missing=%q err=%v", got, err)
	}
}

package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDirsEnvOverride(t *testing.T) {
	t.Setenv(EnvConfigDir, filepath.Join("some", "cfg"))
	t.Setenv(EnvDataDir, filepath.Join("some", "data"))
	d, err := ResolveDirs()
	if err != nil {
		t.Fatalf("ResolveDirs: %v", err)
	}
	if d.Config != filepath.Join("some", "cfg") {
		t.Errorf("Config = %q, want env override", d.Config)
	}
	if d.Data != filepath.Join("some", "data") {
		t.Errorf("Data = %q, want env override", d.Data)
	}
}

func TestResolveDirsPlatformDefaults(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvDataDir, "")
	d, err := ResolveDirs()
	if err != nil {
		t.Fatalf("ResolveDirs: %v", err)
	}
	for _, dir := range []string{d.Config, d.Data} {
		if !filepath.IsAbs(dir) {
			t.Errorf("%q is not absolute", dir)
		}
		if !strings.Contains(dir, "vincent") {
			t.Errorf("%q does not contain \"vincent\"", dir)
		}
	}
}

func TestDataDirPerPlatform(t *testing.T) {
	home := func() (string, error) { return filepath.Join("home", "u"), nil }
	noEnv := func(string) string { return "" }

	cases := []struct {
		name    string
		goos    string
		getenv  func(string) string
		want    string
		wantErr bool
	}{
		{
			name: "windows",
			goos: "windows",
			getenv: func(k string) string {
				if k == "LOCALAPPDATA" {
					return filepath.Join("C:", "Users", "u", "AppData", "Local")
				}
				return ""
			},
			want: filepath.Join("C:", "Users", "u", "AppData", "Local", "vincent"),
		},
		{
			name:    "windows without LOCALAPPDATA",
			goos:    "windows",
			getenv:  noEnv,
			wantErr: true,
		},
		{
			name:   "darwin",
			goos:   "darwin",
			getenv: noEnv,
			want:   filepath.Join("home", "u", "Library", "Application Support", "vincent", "data"),
		},
		{
			name: "linux with XDG_DATA_HOME",
			goos: "linux",
			getenv: func(k string) string {
				if k == "XDG_DATA_HOME" {
					return filepath.Join("xdg", "data")
				}
				return ""
			},
			want: filepath.Join("xdg", "data", "vincent"),
		},
		{
			name:   "linux without XDG_DATA_HOME",
			goos:   "linux",
			getenv: noEnv,
			want:   filepath.Join("home", "u", ".local", "share", "vincent"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dataDir(tc.goos, tc.getenv, home)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("dataDir: %v", err)
			}
			if got != tc.want {
				t.Errorf("dataDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDataDirHomeError(t *testing.T) {
	homeErr := errors.New("no home")
	_, err := dataDir("linux", func(string) string { return "" }, func() (string, error) { return "", homeErr })
	if !errors.Is(err, homeErr) {
		t.Errorf("err = %v, want wrapped %v", err, homeErr)
	}
}

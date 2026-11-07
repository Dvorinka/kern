package workflows

import (
	"strings"
	"testing"
)

func TestCompileServices(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: self-hosted
    services:
      pg:
        image: postgres:16
        env: {POSTGRES_PASSWORD: x}
        options: "--health-cmd 'pg_isready -U postgres' --health-interval 5s --privileged"
      bad:
        image: ""
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("empty service image should be invalid")
	}
	j := findJob(t, res, "j")
	var pg *ServicePlan
	for i := range j.Services {
		if j.Services[i].Name == "pg" {
			pg = &j.Services[i]
		}
	}
	if pg == nil {
		t.Fatalf("pg service missing: %+v", j.Services)
	}
	if pg.Image != "postgres:16" || pg.Env["POSTGRES_PASSWORD"] != "x" {
		t.Fatalf("service spec: %+v", pg)
	}
	if len(pg.Options) != 4 || pg.Options[0] != "--health-cmd" {
		t.Fatalf("health options: %v", pg.Options)
	}
	if !hasDiagnostic(res, "services") {
		t.Fatalf("want services diagnostics (bad image + --privileged warn), got %+v", res.Diagnostics)
	}
}

func TestCompileServicePorts(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: self-hosted
    services:
      pg:
        image: postgres:16
        ports: ["5432:5432"]
      cache:
        image: redis:7
        ports: ["127.0.0.1:6379:6379"]
      remap:
        image: postgres:16
        ports: ["15432:5432"]
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	j := findJob(t, res, "j")
	got := map[string][]string{}
	for _, s := range j.Services {
		got[s.Name] = s.Ports
	}
	if len(got["pg"]) != 1 || got["pg"][0] != "5432" {
		t.Fatalf("pg ports: %v", got["pg"])
	}
	if len(got["cache"]) != 1 || got["cache"][0] != "6379" {
		t.Fatalf("cache ports: %v", got["cache"])
	}
	if len(got["remap"]) != 1 || got["remap"][0] != "5432" {
		t.Fatalf("remap ports: %v", got["remap"])
	}
	found := false
	for _, dg := range res.Diagnostics {
		if dg.Level == "warning" && dg.Code == "services" && strings.Contains(dg.Message, "15432:5432") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want host-remap warning, got %+v", res.Diagnostics)
	}
}

func TestCompileServiceCredentials(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: self-hosted
    services:
      reg:
        image: registry.internal:5000/db:16
        credentials: {username: bot, password: s3cret}
      partial:
        image: postgres:16
        credentials: {username: only-user}
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	j := findJob(t, res, "j")
	var reg, partial *ServicePlan
	for i := range j.Services {
		switch j.Services[i].Name {
		case "reg":
			reg = &j.Services[i]
		case "partial":
			partial = &j.Services[i]
		}
	}
	if reg == nil || reg.Credentials == nil ||
		reg.Credentials.Username != "bot" || reg.Credentials.Password != "s3cret" {
		t.Fatalf("reg credentials: %+v", reg)
	}
	if partial == nil || partial.Credentials != nil {
		t.Fatalf("partial credentials should be dropped: %+v", partial)
	}
	found := false
	for _, dg := range res.Diagnostics {
		if dg.Code == "services" && strings.Contains(dg.Message, "username and password") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want partial-credentials warning, got %+v", res.Diagnostics)
	}
}

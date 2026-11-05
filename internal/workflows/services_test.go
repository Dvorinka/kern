package workflows

import "testing"

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

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"accorda/internal/core/plan"
)

func TestWritePlan_Format(t *testing.T) {
	p := plan.New("", "acme/infra", "a84fd21", time.Unix(0, 0)).
		AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "api", Image: "api:2"}).
		AddAction(plan.NoopFor("worker"))

	var buf bytes.Buffer
	writePlan(&buf, p)
	out := buf.String()
	for _, want := range []string{
		"Deployment plan\n",
		"deployment= environment=acme/infra commit=a84fd21\n",
		"api          CHANGED\n",
		"worker       UNCHANGED\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

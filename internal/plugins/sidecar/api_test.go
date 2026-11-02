package sidecar

import (
	"reflect"
	"testing"
)

func TestEnvForOwnerWins(t *testing.T) {
	r := LaunchResult{
		Env: []string{"A=owner", "K=owner", "B=other", "K=other"},
		EnvBy: map[string][]string{
			"owner": {"A=owner", "K=owner"},
			"other": {"B=other", "K=other"},
		},
	}
	got := r.EnvFor("owner")
	want := []string{"B=other", "A=owner", "K=owner"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnvFor(owner) = %v, want %v", got, want)
	}
	if got := r.EnvFor(""); !reflect.DeepEqual(got, r.Env) {
		t.Fatalf("EnvFor(\"\") = %v", got)
	}
}

package store

import (
	"github.com/p1ngu/rmm/internal/models"
	"testing"
)

func TestDispatchRequiresTheExactCurrentRelease(t *testing.T) {
	sc := &models.Script{Shell: "sh", Body: "printf status", Params: []models.ScriptParam{}}
	hash := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params)
	sc.AIReleasedHash = hash
	j := &models.Job{Shell: sc.Shell, Script: sc.Body}
	if err := validateAutonomousRelease(sc, j, hash); err != nil {
		t.Fatalf("valid control: %v", err)
	}
	for _, mutate := range []func(){
		func() { sc.AIReleasedHash = "" },
		func() { sc.Body = "printf changed" },
		func() { sc.Params = []models.ScriptParam{{Name: "mode", Kind: "enum", Values: []string{"safe"}}} },
	} {
		cp := *sc
		mutate()
		if err := validateAutonomousRelease(sc, j, hash); err == nil {
			t.Fatal("stale release dispatched")
		}
		*sc = cp
	}
	if err := validateAutonomousRelease(sc, j, "different"); err == nil {
		t.Fatal("different release hash dispatched")
	}
}

func TestAutonomousDispatchRejectsDifferentVariableSnapshot(t *testing.T) {
	sc := &models.Script{Shell: "sh", Body: "echo ok", Variables: []string{"CUSTOM_KEY"}}
	sc.AIReleasedHash = models.ScriptContentHash(sc.Shell, sc.Body, sc.Params, sc.Variables)
	j := &models.Job{Shell: sc.Shell, Script: sc.Body, Variables: []string{"OTHER_KEY"}}
	if err := validateAutonomousRelease(sc, j, sc.AIReleasedHash); err == nil {
		t.Fatal("dispatch accepted names outside the reviewed release")
	}
	j.Variables = []string{"CUSTOM_KEY"}
	if err := validateAutonomousRelease(sc, j, sc.AIReleasedHash); err != nil {
		t.Fatal(err)
	}
}

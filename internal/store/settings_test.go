package store

import (
	"context"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)

	if _, _, ok, err := db.GetSetting(ctx, "ai.model"); err != nil || ok {
		t.Fatalf("missing setting: %v %v", ok, err)
	}
	if err := db.SetSetting(ctx, "ai.model", "claude-opus-5-5", at); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "guidance.global", "Prefer small diffs.", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "guidance.repo.r_1", "Go code must have tests.", at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, when, ok, err := db.GetSetting(ctx, "ai.model")
	if err != nil || !ok || v != "claude-opus-5-5" || !when.Equal(at) {
		t.Fatalf("GetSetting = %q %v %v %v", v, when, ok, err)
	}
	if err := db.SetSetting(ctx, "ai.model", "claude-sonnet-5-5", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v, when, _, _ := db.GetSetting(ctx, "ai.model"); v != "claude-sonnet-5-5" || !when.Equal(at.Add(time.Hour)) {
		t.Errorf("after replace: %q %v", v, when)
	}
	all, err := db.ListSettings(ctx, "guidance.")
	if err != nil || len(all) != 2 || all["guidance.global"].Value != "Prefer small diffs." || all["guidance.repo.r_1"].Value != "Go code must have tests." {
		t.Fatalf("ListSettings = %+v %v", all, err)
	}
	if err := db.DeleteSetting(ctx, "guidance.global"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := db.GetSetting(ctx, "guidance.global"); ok {
		t.Error("deleted setting still there")
	}
	if err := db.DeleteSetting(ctx, "nope"); err != nil {
		t.Errorf("deleting a missing setting: %v", err)
	}
}

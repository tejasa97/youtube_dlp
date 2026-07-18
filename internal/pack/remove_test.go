//go:build darwin || linux

package pack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveInactiveAndActiveVersions(t *testing.T) {
	t.Run("inactive previous", func(t *testing.T) {
		root := installTwoVersions(t)
		receipt, err := Remove(context.Background(), root, "fixture-pack", "1.0.0", installPolicy(t), RemoveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.State.Current != "2.0.0" || receipt.State.Previous != "" || receipt.RemovedVersion != "1.0.0" {
			t.Fatalf("unexpected removal state: %#v", receipt)
		}
		if _, err := os.Lstat(filepath.Join(root, "fixture-pack", "versions", "1.0.0")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed version still exists: %v", err)
		}
	})
	t.Run("active without replacement", func(t *testing.T) {
		root := canonicalTestRoot(t)
		if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, installPolicy(t), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		receipt, err := Remove(context.Background(), root, "fixture-pack", "1.0.0", installPolicy(t), RemoveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.State.Current != "" || receipt.State.Previous != "" || len(receipt.State.Records) != 0 {
			t.Fatalf("unexpected empty state: %#v", receipt.State)
		}
	})
	t.Run("active with verified previous", func(t *testing.T) {
		root := installTwoVersions(t)
		receipt, err := Remove(context.Background(), root, "fixture-pack", "2.0.0", installPolicy(t), RemoveOptions{ActivatePrevious: true})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.State.Current != "1.0.0" || receipt.State.Previous != "" || receipt.Replacement != "1.0.0" {
			t.Fatalf("unexpected replacement state: %#v", receipt)
		}
	})
}

func TestRemoveActivationRequiresPermissionReview(t *testing.T) {
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	first := fixtureArchive(t, "1.0.0", "one", PermissionNetwork)
	if _, err := Install(context.Background(), first, root, policy, InstallOptions{ApprovePermissionIncrease: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "two"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	receipt, err := Remove(context.Background(), root, "fixture-pack", "2.0.0", policy, RemoveOptions{ActivatePrevious: true})
	if !errors.Is(err, ErrPermissionReview) || !receipt.Review.Increase() || receipt.Review.Added[0] != PermissionNetwork {
		t.Fatalf("permission review = %#v, error = %v", receipt.Review, err)
	}
	if _, err := Remove(context.Background(), root, "fixture-pack", "2.0.0", policy, RemoveOptions{ActivatePrevious: true, ApprovePermissionIncrease: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDoesNotFollowHostilePayloadLinks(t *testing.T) {
	for _, linkType := range []string{"symlink", "hardlink"} {
		t.Run(linkType, func(t *testing.T) {
			root := canonicalTestRoot(t)
			policy := installPolicy(t)
			if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(root), "outside")
			if err := os.WriteFile(outside, []byte("outside remains"), 0o600); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(root, "fixture-pack", "versions", "1.0.0", "bin", "fixture")
			if err := os.Remove(payload); err != nil {
				t.Fatal(err)
			}
			var err error
			if linkType == "symlink" {
				err = os.Symlink(outside, payload)
			} else {
				err = os.Link(outside, payload)
			}
			if err != nil {
				t.Skipf("link unavailable: %v", err)
			}
			if _, err := Remove(context.Background(), root, "fixture-pack", "1.0.0", policy, RemoveOptions{}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(outside)
			if err != nil || string(body) != "outside remains" {
				t.Fatalf("outside link target changed: %q, %v", body, err)
			}
		})
	}
}

func TestRemoveCancellationAndPendingJournalRecovery(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		root := canonicalTestRoot(t)
		policy := installPolicy(t)
		if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Remove(ctx, root, "fixture-pack", "1.0.0", policy, RemoveOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Remove() error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "fixture-pack", "versions", "1.0.0")); err != nil {
			t.Fatalf("cancelled removal changed payload: %v", err)
		}
	})
	t.Run("crash journal", func(t *testing.T) {
		root := canonicalTestRoot(t)
		policy := installPolicy(t)
		if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		packRoot := filepath.Join(root, "fixture-pack")
		state, err := loadState(packRoot, "fixture-pack")
		if err != nil {
			t.Fatal(err)
		}
		state.Records = removeRecord(state.Records, "1.0.0")
		state.Current = ""
		state.Previous = ""
		state.PendingRemovals = []string{"1.0.0"}
		state.LastTransition = Transition{From: "1.0.0", Reason: "remove", At: policy.Now.UTC().Format("2006-01-02T15:04:05Z")}
		if err := writeState(packRoot, state); err != nil {
			t.Fatal(err)
		}
		versionsRoot := filepath.Join(packRoot, "versions")
		quarantine := filepath.Join(versionsRoot, ".remove-crashed-transaction")
		if err := os.Rename(filepath.Join(versionsRoot, "1.0.0"), quarantine); err != nil {
			t.Fatal(err)
		}
		if err := syncDirectory(versionsRoot); err != nil {
			t.Fatal(err)
		}
		// Installing another version must finish the journaled unlink before
		// publishing the new state. This models a crash at the exact point
		// after version-to-quarantine rename but before quarantine deletion.
		receipt, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "two"), root, policy, InstallOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(receipt.State.PendingRemovals) != 0 || receipt.State.Current != "2.0.0" {
			t.Fatalf("pending removal was not recovered: %#v", receipt.State)
		}
		if _, err := os.Lstat(filepath.Join(root, "fixture-pack", "versions", "1.0.0")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("journaled version remains: %v", err)
		}
		if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("crashed removal quarantine remains: %v", err)
		}
	})
}

func TestRemoveRejectsUnknownVersion(t *testing.T) {
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(context.Background(), root, "fixture-pack", "9.0.0", policy, RemoveOptions{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("unknown removal error = %v", err)
	}
}

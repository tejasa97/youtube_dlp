//go:build darwin || linux

package pack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func canonicalTestRoot(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(real, "packs")
}

func installPolicy(t *testing.T) VerifyPolicy {
	t.Helper()
	return fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
}

func TestInstallUpgradeAndVerifiedRollback(t *testing.T) {
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	first := fixtureArchive(t, "1.0.0", "version one")
	firstReceipt, err := Install(context.Background(), first, root, policy, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if firstReceipt.State.Current != "1.0.0" || firstReceipt.State.Previous != "" {
		t.Fatalf("unexpected first state: %#v", firstReceipt.State)
	}
	body, err := os.ReadFile(filepath.Join(firstReceipt.Path, "bin", "fixture"))
	if err != nil || string(body) != "version one" {
		t.Fatalf("installed body = %q, error = %v", body, err)
	}
	second := fixtureArchive(t, "2.0.0", "version two")
	secondReceipt, err := Install(context.Background(), second, root, policy, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secondReceipt.State.Current != "2.0.0" || secondReceipt.State.Previous != "1.0.0" || secondReceipt.Transition.Reason != "install" {
		t.Fatalf("unexpected upgraded state: %#v", secondReceipt.State)
	}
	rolledBack, err := Rollback(context.Background(), root, "fixture-pack", policy)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State.Current != "1.0.0" || rolledBack.State.Previous != "2.0.0" || rolledBack.Transition.Reason != "rollback" {
		t.Fatalf("unexpected rollback state: %#v", rolledBack.State)
	}
	if string(rolledBack.Verified.Payload["bin/fixture"]) != "version one" {
		t.Fatal("rollback did not verify the target payload")
	}
}

func TestInstallPermissionIncreaseRequiresExplicitApproval(t *testing.T) {
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	upgrade := fixtureArchive(t, "2.0.0", "two", PermissionNetwork, PermissionSecrets)
	receipt, err := Install(context.Background(), upgrade, root, policy, InstallOptions{})
	if !errors.Is(err, ErrPermissionReview) {
		t.Fatalf("Install() error = %v, want permission review", err)
	}
	if !receipt.Review.Increase() || len(receipt.Review.Added) != 2 {
		t.Fatalf("missing review data: %#v", receipt.Review)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "fixture-pack", "versions", "2.0.0")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unapproved payload was installed: %v", statErr)
	}
	approved, err := Install(context.Background(), upgrade, root, policy, InstallOptions{ApprovePermissionIncrease: true})
	if err != nil {
		t.Fatal(err)
	}
	if approved.State.Current != "2.0.0" || !approved.Review.Increase() {
		t.Fatalf("unexpected approved receipt: %#v", approved)
	}
}

func TestInstallRejectsDowngradeDuplicateAndLock(t *testing.T) {
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	if _, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "two"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "one"), root, policy, InstallOptions{}); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade error = %v", err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "two"), root, policy, InstallOptions{}); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("duplicate error = %v", err)
	}
	lock := filepath.Join(root, "fixture-pack", ".install.lock")
	if err := os.WriteFile(lock, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "3.0.0", "three"), root, policy, InstallOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked error = %v", err)
	}
}

func TestRollbackRejectsRevokedExpiredAndTamperedTargets(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		root := installTwoVersions(t)
		policy := installPolicy(t)
		policy.Revocations.Packages = []PackageRevocation{{Name: "fixture-pack", Version: "1.0.0"}}
		if _, err := Rollback(context.Background(), root, "fixture-pack", policy); !errors.Is(err, ErrRevoked) {
			t.Fatalf("Rollback() error = %v, want revoked", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		root := installTwoVersions(t)
		policy := installPolicy(t)
		policy.Now = time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := Rollback(context.Background(), root, "fixture-pack", policy); !errors.Is(err, ErrExpired) {
			t.Fatalf("Rollback() error = %v, want expired", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := installTwoVersions(t)
		target := filepath.Join(root, "fixture-pack", "versions", "1.0.0", "bin", "fixture")
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../README.txt", target); err != nil {
			t.Fatal(err)
		}
		if _, err := Rollback(context.Background(), root, "fixture-pack", installPolicy(t)); !errors.Is(err, ErrCorruptInstall) {
			t.Fatalf("Rollback() error = %v, want corrupt install", err)
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := installTwoVersions(t)
		target := filepath.Join(root, "fixture-pack", "versions", "1.0.0", "bin", "fixture")
		external := filepath.Join(filepath.Dir(root), "external")
		if err := os.WriteFile(external, []byte("version one"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(external, target); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if _, err := Rollback(context.Background(), root, "fixture-pack", installPolicy(t)); !errors.Is(err, ErrCorruptInstall) {
			t.Fatalf("Rollback() error = %v, want corrupt install", err)
		}
	})
	t.Run("extra file", func(t *testing.T) {
		root := installTwoVersions(t)
		extra := filepath.Join(root, "fixture-pack", "versions", "1.0.0", "unexpected")
		if err := os.WriteFile(extra, []byte("hostile"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Rollback(context.Background(), root, "fixture-pack", installPolicy(t)); !errors.Is(err, ErrCorruptInstall) {
			t.Fatalf("Rollback() error = %v, want corrupt install", err)
		}
	})
}

func installTwoVersions(t *testing.T) string {
	t.Helper()
	root := canonicalTestRoot(t)
	policy := installPolicy(t)
	if _, err := Install(context.Background(), fixtureArchive(t, "1.0.0", "version one"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "version two"), root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInstallRejectsUnsafeRootsAndState(t *testing.T) {
	policy := installPolicy(t)
	archive := fixtureArchive(t, "1.0.0", "one")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), archive, rootLink, policy, InstallOptions{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink root error = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed: %v %v", info.Mode().Perm(), err)
	}

	root := canonicalTestRoot(t)
	if _, err := Install(context.Background(), archive, root, policy, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "fixture-pack", "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside-state"), statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), fixtureArchive(t, "2.0.0", "two"), root, policy, InstallOptions{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink state error = %v", err)
	}
}

func TestInstallCancelledBeforeFilesystemMutation(t *testing.T) {
	root := canonicalTestRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Install(ctx, fixtureArchive(t, "1.0.0", "one"), root, installPolicy(t), InstallOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled install mutated root: %v", err)
	}
}

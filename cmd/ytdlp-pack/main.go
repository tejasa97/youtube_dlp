package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

const maximumPackInput = 80 << 20

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "ytdlp-pack %s\n", ytdlp.APIVersion)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ytdlp-pack <verify|install|rollback|remove> [options]")
		return 2
	}
	operation, args := args[0], args[1:]
	flags := flag.NewFlagSet("ytdlp-pack "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	archivePath := flags.String("archive", "", "signed pack archive (verify/install)")
	publicKeyHex := flags.String("public-key", "", "trusted Ed25519 public key in hex")
	nowText := flags.String("now", "", "verification time as canonical UTC RFC3339")
	hostVersion := flags.String("host-version", "", "current host version")
	currentVersion := flags.String("current-version", "", "currently installed version for downgrade checks")
	root := flags.String("root", "", "private absolute pack installation root")
	name := flags.String("name", "", "pack name (rollback/remove)")
	version := flags.String("pack-version", "", "pack version (remove)")
	approve := flags.Bool("approve-permission-increase", false, "approve the exact reported permission increase")
	activatePrevious := flags.Bool("activate-previous", false, "activate the verified previous version when removing the active version")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	trust, err := parseTrust(*publicKeyHex, *nowText, *hostVersion, *currentVersion)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-pack: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	switch operation {
	case "verify", "install":
		archive, err := readBounded(*archivePath)
		if err != nil {
			fmt.Fprintf(stderr, "ytdlp-pack: %v\n", err)
			return 2
		}
		if operation == "verify" {
			descriptor, err := ytdlp.VerifyPluginPack(archive, trust)
			if err != nil {
				return report(stderr, err)
			}
			if err := encoder.Encode(descriptor); err != nil {
				return 1
			}
			return 0
		}
		if *root == "" {
			fmt.Fprintln(stderr, "ytdlp-pack: --root is required for install")
			return 2
		}
		installed, review, err := ytdlp.InstallPluginPack(ctx, archive, *root, trust, ytdlp.PluginPackInstallOptions{ApprovePermissionIncrease: *approve})
		if err != nil {
			if review.Increase() {
				_ = json.NewEncoder(stderr).Encode(review)
			}
			return report(stderr, err)
		}
		return encodeResult(encoder, installed.Descriptor(), review)
	case "rollback":
		if *root == "" || *name == "" {
			fmt.Fprintln(stderr, "ytdlp-pack: --root and --name are required for rollback")
			return 2
		}
		installed, review, err := ytdlp.RollbackPluginPack(ctx, *root, *name, trust, ytdlp.PluginPackRollbackOptions{ApprovePermissionIncrease: *approve})
		if err != nil {
			if review.Increase() {
				_ = json.NewEncoder(stderr).Encode(review)
			}
			return report(stderr, err)
		}
		return encodeResult(encoder, installed.Descriptor(), review)
	case "remove":
		if *root == "" || *name == "" || *version == "" {
			fmt.Fprintln(stderr, "ytdlp-pack: --root, --name and --pack-version are required for remove")
			return 2
		}
		state, review, err := ytdlp.RemovePluginPack(ctx, *root, *name, *version, trust, ytdlp.PluginPackRemoveOptions{
			ActivatePrevious: *activatePrevious, ApprovePermissionIncrease: *approve,
		})
		if err != nil {
			if review.Increase() {
				_ = json.NewEncoder(stderr).Encode(review)
			}
			return report(stderr, err)
		}
		if err := encoder.Encode(struct {
			State  ytdlp.PackState            `json:"state"`
			Review ytdlp.PackPermissionReview `json:"review"`
		}{state, review}); err != nil {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "ytdlp-pack: unknown operation %q\n", operation)
		return 2
	}
}

func parseTrust(publicKeyHex, nowText, hostVersion, currentVersion string) (ytdlp.PluginPackTrust, error) {
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return ytdlp.PluginPackTrust{}, errors.New("--public-key must be a 32-byte Ed25519 key in hex")
	}
	now, err := time.Parse(time.RFC3339, nowText)
	if err != nil || now.UTC().Format(time.RFC3339) != nowText {
		return ytdlp.PluginPackTrust{}, errors.New("--now must be canonical UTC RFC3339")
	}
	publicKey := ed25519.PublicKey(key)
	keyID, err := ytdlp.PluginPackKeyID(publicKey)
	if err != nil {
		return ytdlp.PluginPackTrust{}, err
	}
	return ytdlp.PluginPackTrust{
		Keys: map[string]ed25519.PublicKey{keyID: publicKey}, Now: now,
		HostVersion: hostVersion, CurrentVersion: currentVersion,
	}, nil
}

func readBounded(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("--archive is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open archive failed")
	}
	defer file.Close()
	limited := io.LimitReader(file, maximumPackInput+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > maximumPackInput {
		return nil, errors.New("archive is unreadable or oversized")
	}
	return body, nil
}

func encodeResult(encoder *json.Encoder, descriptor ytdlp.PluginDescriptor, review ytdlp.PackPermissionReview) int {
	if err := encoder.Encode(struct {
		Plugin ytdlp.PluginDescriptor     `json:"plugin"`
		Review ytdlp.PackPermissionReview `json:"review"`
	}{descriptor, review}); err != nil {
		return 1
	}
	return 0
}

func report(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "ytdlp-pack: %v\n", err)
	if ytdlp.IsCategory(err, ytdlp.ErrorCancelled) {
		return 130
	}
	if ytdlp.IsCategory(err, ytdlp.ErrorUnsupported) {
		return 3
	}
	if ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput) {
		return 2
	}
	if ytdlp.IsCategory(err, ytdlp.ErrorSecurity) {
		return 6
	}
	return 1
}

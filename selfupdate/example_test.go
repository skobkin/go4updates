package selfupdate_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/selfupdate"
	"github.com/skobkin/go4updates/source/github"
	"github.com/skobkin/go4updates/watch"
)

// ExampleUpdater_Apply performs a one-shot check-and-update in a CLI or
// headless program. The checker decides that an update exists; the updater
// selects, downloads, verifies, extracts, and installs the matching asset.
func ExampleUpdater_Apply() {
	source, err := github.New("example-org/example-app")
	if err != nil {
		log.Fatal(err)
	}

	checker := updates.NewChecker(updates.CheckerOptions{})

	result, err := checker.Check(context.Background(), updates.Target{
		CurrentVersion: "1.2.2",
		Source:         source,
	})
	if err != nil {
		log.Fatal(err)
	}

	if result.Status != updates.StatusUpdateAvailable {
		log.Println("no update available")

		return
	}

	updater, err := selfupdate.New(selfupdate.WithAppName("example-app"))
	if err != nil {
		log.Fatal(err)
	}

	// Optional: preview the asset the platform policy picked before
	// downloading anything.
	asset, err := updater.SelectAsset(*result.Latest)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("installing %s from %s", result.Latest.Version, asset.Name)

	applied, err := updater.Apply(context.Background(), result)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("installed %s; previous binary kept at %s", applied.Version, applied.Backup)

	// After restarting into the new version and confirming it is healthy
	// (initialization or a health check), retire the backup:
	if err := updater.Cleanup(context.Background()); err != nil {
		log.Printf("cleanup: %v", err)
	}
}

// Example_updaterWithWatcher applies updates for a periodically checked
// desktop application. The application decides whether to apply — through
// user confirmation, a maintenance window, or policy; the library never
// applies updates on its own. The backup of the previous version is removed
// by Cleanup only after the new version started up healthily.
func Example_updaterWithWatcher() {
	source, err := github.New("example-org/example-app")
	if err != nil {
		log.Fatal(err)
	}

	checker := updates.NewChecker(updates.CheckerOptions{})

	target := updates.Target{
		CurrentVersion: "1.2.2",
		Source:         source,
	}

	watcher := watch.New(checker, watch.Options{Interval: time.Hour})

	if err := watcher.Add("app", target); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = watcher.Run(ctx)
	}()

	events, stop := watcher.Subscribe()
	defer stop()

	updater, err := selfupdate.New(selfupdate.WithAppName("example-app"))
	if err != nil {
		log.Fatal(err)
	}

	for event := range events {
		result := event.State.Result

		if result == nil || result.Status != updates.StatusUpdateAvailable {
			continue
		}

		// Application policy decides whether to apply.
		if !updateConfirmedByUser(result.Latest.Version) {
			continue
		}

		applied, err := updater.Apply(ctx, *result)
		if err != nil {
			log.Printf("update failed: %v", err)

			continue
		}

		log.Printf("installed %s; restart to run it, then call updater.Cleanup after a healthy startup (backup: %s)",
			applied.Version, applied.Backup)
	}
}

func updateConfirmedByUser(string) bool {
	// Prompt the user or consult application policy.
	return false
}

// ed25519Verifier is a custom [selfupdate.Verifier] checking a detached
// ed25519 signature published as a sibling ".sig" asset over the SHA-256
// digest of the downloaded archive. Signature verification authenticates
// the publisher; the checksum covered by the signature provides the
// integrity check for the archive bytes.
//
// The signature asset is fetched through [selfupdate.Downloader], so the
// package's HTTPS-only transport policy (enforced on every redirect hop)
// and size bounds apply to the signature too: fetching it with a bare
// http.Client would bypass both, defeating the publisher-authentication
// guarantee this verifier exists to provide.
type ed25519Verifier struct {
	// publicKey is the publisher's ed25519 public key, embedded in the
	// application binary.
	publicKey ed25519.PublicKey
}

func (v ed25519Verifier) Verify(ctx context.Context, release updates.Release, asset updates.Asset, data io.Reader) error {
	var signature *updates.Asset

	for _, a := range release.Assets {
		if a.Name == asset.Name+".sig" {
			signature = &a

			break
		}
	}

	if signature == nil {
		return errors.New("no signature asset published for " + asset.Name)
	}

	downloader, err := selfupdate.NewDownloader(selfupdate.WithMaxAssetBytes(64 << 10))
	if err != nil {
		return err
	}

	var sig bytes.Buffer

	if err := downloader.Download(ctx, *signature, &sig); err != nil {
		return err
	}

	digest := sha256.New()

	if _, err := io.Copy(digest, data); err != nil {
		return err
	}

	if !ed25519.Verify(v.publicKey, digest.Sum(nil), sig.Bytes()) {
		return errors.New("signature mismatch")
	}

	return nil
}

// Example_verifierEd25519 wires the ed25519 signature verifier into an
// Updater, replacing checksum-only verification with publisher
// authentication. The public key is embedded at build time, typically via
// -ldflags "-X main.publisherKey=<hex>".
func Example_verifierEd25519() {
	keyBytes, err := hex.DecodeString("d75a980182b2ab7f11863efea7067fbe1dc1a0b1d0e7a54ee6a5e80b1d6c2e09")
	if err != nil {
		log.Fatal(err)
	}

	updater, err := selfupdate.New(
		selfupdate.WithAppName("example-app"),
		selfupdate.WithVerifier(ed25519Verifier{publicKey: ed25519.PublicKey(keyBytes)}),
	)
	if err != nil {
		log.Fatal(err)
	}

	_ = updater
}

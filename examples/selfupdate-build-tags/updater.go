//go:build !no_selfupdate

package main

import (
	"context"
	"fmt"
	"log"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/selfupdate"
)

// applySelfUpdate is the enabled wiring: standalone builds install updates
// through the selfupdate package. The preflight disables the action when
// the environment cannot take an update, and progress goes to the log.
//
// The default build (no tags) compiles this file.
func applySelfUpdate(ctx context.Context, result updates.Result) error {
	if result.Status != updates.StatusUpdateAvailable {
		return nil
	}

	updater, err := selfupdate.New(selfupdate.WithAppName(appName))
	if err != nil {
		return err
	}

	// Advisory preflight: obvious blockers — an unconfirmed backup, an
	// unusable target, an unwritable directory — fail here, before the
	// download. Not a guarantee; the apply can still fail later.
	if err := updater.CheckInstallability(ctx); err != nil {
		return fmt.Errorf("self-update is unavailable: %w", err)
	}

	applied, err := updater.ApplyWithProgress(ctx, result, func(p selfupdate.Progress) {
		switch p.Stage {
		case selfupdate.StageDownloading, selfupdate.StageVerifying, selfupdate.StageExtracting:
			if p.Total == 0 {
				log.Printf("%s: %d bytes (total unknown)", p.Stage, p.Current)

				return
			}

			log.Printf("%s: %d of %d bytes", p.Stage, p.Current, p.Total)

		default:
			// StageInstalling: stop offering cancellation from here on.
			// StageCompleted: the update is fully installed.
			log.Printf("%s", p.Stage)
		}
	})
	if err != nil {
		return err
	}

	log.Printf("installed %s; restart to run it, then retire the backup after a healthy startup (backup: %s)",
		applied.Version, applied.Backup)

	return nil
}

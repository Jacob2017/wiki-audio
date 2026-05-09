// Package-level rationale (placed on newPublishCmd's doc since
// internal/cli has its own package doc on doc.go).
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Jacob2017/wiki-audio/internal/cache"
	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/feed"
	"github.com/Jacob2017/wiki-audio/internal/manifest"
	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/publish"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// publishKeys collects the canonical R2 keys the publish path
// touches at the bucket root. Episodes live under
// publish.EpisodePrefix; the manifest + feed land at the bucket
// root.
const (
	feedKey         = "pg.xml"
	feedContentType = "application/rss+xml"
	mp3ContentType  = "audio/mpeg"
)

// errPublishNoToken is returned when WIKI_AUDIO_ACCESS_TOKEN is empty
// at publish time. Surfaced BEFORE any upload so a partial run
// can't strand objects in R2 without a corresponding stamped feed.
var errPublishNoToken = errors.New(
	"publish: WIKI_AUDIO_ACCESS_TOKEN is empty (run wiki-audio doctor)")

// publishFlags carries the user-facing flags. --feed-only (wa-i1l.8)
// and --dry-run (wa-i1l.9) are mutually exclusive modes; --prune
// (wa-i1l.10) is an additive flag that only makes sense alongside
// the default mode (the only one that computes Plan.Stale and
// performs writes).
type publishFlags struct {
	feedOnly bool
	dryRun   bool
	prune    bool
}

func newPublishCmd() *cobra.Command {
	flags := &publishFlags{}
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Diff + upload MP3s + regenerate RSS feed",
		Long: "publish reconciles ~/.cache/wiki-audio/out/ against R2: " +
			"computes the diff (missing / overwrite / unchanged / stale), " +
			"uploads new and changed MP3s, regenerates pg.xml with token-" +
			"stamped enclosure URLs, and writes the manifest + feed back " +
			"to R2. Atomicity is best-effort — see publish.go for the " +
			"upload-order contract.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPublish(cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.feedOnly, "feed-only", false,
		"regenerate and upload pg.xml only; skip MP3 diff + upload "+
			"(used after token rotation)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"compute and print the diff, do NOT upload anything")
	cmd.Flags().BoolVar(&flags.prune, "prune", false,
		"delete R2 objects under pg/ that no manifest entry claims "+
			"(off by default — stale objects are usually old episodes "+
			"that were renamed or unindexed; opt in explicitly)")
	cmd.MarkFlagsMutuallyExclusive("feed-only", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("feed-only", "prune")
	cmd.MarkFlagsMutuallyExclusive("dry-run", "prune")
	return cmd
}

// runPublish is the §3 / wa-i1l.7 default-path orchestrator:
//
//  1. Load Config + .env.
//  2. Construct r2.Client.
//  3. manifest.Load from R2.
//  4. publish.Diff against R2 → Plan.
//  5. For each ToUpload + ToOverwrite: read from
//     ~/.cache/wiki-audio/out/<slug>.mp3 → r2.PutWithRetries
//     → patch the manifest entry's R2Key/R2ETag/PublishedAt.
//  6. feed.Generate over the now-current Manifest entries.
//  7. feed.StampTokens(feedXML, WIKI_AUDIO_ACCESS_TOKEN).
//  8. PutObject pg.xml + manifest.Save (manifest first per §6
//     "Manifest JSON corruption" — a half-published feed pointing
//     at a missing manifest is recoverable; a feed-stamped manifest
//     pointing at missing MP3s isn't).
//  9. Print "feed live at <BaseURL>/pg.xml?t=<token>".
//
// Atomicity caveat: there's no R2-level transaction across multiple
// objects. Order: MP3s → manifest → feed. If a partial run exits
// after step 5 but before step 8, the OLD manifest in R2 is still
// authoritative; the next publish run re-runs Diff against R2 and
// re-detects the still-needed uploads. Worst case: a few extra HEAD
// calls on the next run (cheap). Document this contract in any
// future refactor — swapping the order would corrupt the recovery
// story.
//
// publishMode selects which subset of the orchestrator runs.
// ModeFeedOnly skips Diff + uploads (used after token rotation —
// only the feed needs regeneration). ModeDryRun stops after Diff
// without making any R2 writes.
type publishMode int

const (
	modeDefault publishMode = iota
	modeFeedOnly
	modeDryRun
)

func (m publishMode) String() string {
	switch m {
	case modeFeedOnly:
		return "feed-only"
	case modeDryRun:
		return "dry-run"
	default:
		return "default"
	}
}

// runPublish wires the cobra command surface into runPublishCore.
// Flag parsing → mode selection → live R2 client construction.
// runPublishCore is the pure-logic path that publish_test.go
// drives with an r2.Fake.
func runPublish(cmd *cobra.Command, flags *publishFlags) error {
	mode := modeDefault
	switch {
	case flags.feedOnly:
		mode = modeFeedOnly
	case flags.dryRun:
		mode = modeDryRun
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	configPath, _ := cmd.Flags().GetString("config")
	envPath, _ := cmd.Flags().GetString("env")
	envLocal, _ := cmd.Flags().GetBool("env-local")
	if envLocal {
		envPath = ".env"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := config.LoadEnv(envPath); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	// Pre-flight: WIKI_AUDIO_ACCESS_TOKEN must be present before
	// any R2 write. Stamping the feed without it would silently
	// emit unstamped URLs (StampTokens logs a WARN). Skipped in
	// dry-run mode — the diff doesn't need the token, and the
	// "no writes performed" exit message is more useful than a
	// pre-flight refusal when the operator is just sanity-checking.
	token := strings.TrimSpace(os.Getenv("WIKI_AUDIO_ACCESS_TOKEN"))
	if token == "" && mode != modeDryRun {
		return errPublishNoToken
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2.AccountID)
	store, err := r2.New(endpoint,
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		cfg.R2.Bucket)
	if err != nil {
		return fmt.Errorf("publish: r2.New: %w", err)
	}

	return runPublishCore(ctx, cmd.OutOrStdout(), store, cfg, token, mode, flags.prune, time.Now)
}

// runPublishCore is the orchestrator's pure logic, factored out so
// publish_test.go can drive it with an r2.Fake and a frozen `now`.
//
// Modes:
//   - modeDefault   — full flow: Load, Diff, upload MP3s, regenerate
//                     feed, Save manifest, PUT feed.
//   - modeFeedOnly  — Load + regenerate feed + PUT feed only. Skips
//                     Diff and uploads. Use case: token rotation
//                     (worker secret + .env updated, regen feed with
//                     the new token-stamped URLs without re-uploading
//                     any episodes). Manifest is NOT re-saved — the
//                     manifest content is unchanged in this scenario,
//                     and skipping the Save avoids the .bak rotation
//                     overhead for a no-op write.
//   - modeDryRun    — Load + Diff + print plan; no R2 writes. Use
//                     case: pre-bulk-run sanity check.
//
// Errors propagate to runPublish and exit cobra non-zero. A mid-batch
// error in modeDefault leaves the manifest in R2 unchanged (Save runs
// only after every upload succeeds), preserving the recovery story.
func runPublishCore(
	ctx context.Context,
	out io.Writer,
	store r2.Storage,
	cfg *model.Config,
	token string,
	mode publishMode,
	prune bool,
	nowFn func() time.Time,
) error {
	logger := slog.With("phase", "publish", "bucket", cfg.R2.Bucket, "mode", mode.String())

	mft, err := manifest.Load(ctx, store)
	if err != nil {
		return fmt.Errorf("publish: load manifest: %w", err)
	}
	logger.Info("manifest loaded", "entries", len(mft.Entries))

	switch mode {
	case modeFeedOnly:
		fmt.Fprintln(out, "feed-only mode: skipping diff + MP3 upload")
		return runPublishFeedOnlyTail(ctx, out, store, cfg, token, mft, logger)
	case modeDryRun:
		return runPublishDryRunTail(ctx, out, store, cfg, mft, logger)
	}

	// modeDefault: the full flow.
	plan, err := publish.Diff(ctx, store, mft)
	if err != nil {
		return fmt.Errorf("publish: diff: %w", err)
	}
	fmt.Fprintln(out, plan.String())

	uploads := append([]model.ManifestEntry{}, plan.ToUpload...)
	uploads = append(uploads, plan.ToOverwrite...)
	for _, entry := range uploads {
		if err := uploadOneEpisode(ctx, store, mft, entry, cfg.R2.Bucket, nowFn(), logger, out); err != nil {
			// Per the atomicity caveat: the manifest in R2 is the
			// OLD one, and we have NOT regenerated/uploaded the
			// feed. Whatever MP3s succeeded before this failure are
			// in R2 but the feed doesn't reference them yet; next
			// publish will detect and finish.
			return fmt.Errorf("publish: upload %s: %w", entry.Slug, err)
		}
	}

	overlayLocalFeedFields(mft, logger)

	feedXML, err := buildFeed(mft, cfg, token)
	if err != nil {
		return fmt.Errorf("publish: feed: %w", err)
	}

	// Save manifest BEFORE feed (the upload-order contract). A
	// readable feed pointing at the manifest is the contract; the
	// reverse leaves a stale feed talking about an absent manifest
	// for the recovery window between the two writes.
	if err := manifest.Save(ctx, store, mft); err != nil {
		return fmt.Errorf("publish: save manifest: %w", err)
	}
	logger.Info("manifest saved", "key", manifest.PrimaryKey)

	if _, err := store.PutObject(ctx, feedKey,
		bytes.NewReader(feedXML), int64(len(feedXML)), feedContentType); err != nil {
		return fmt.Errorf("publish: put %s: %w", feedKey, err)
	}
	fmt.Fprintf(out, "regenerating pg.xml (%d items) → r2://%s/%s ✓\n",
		feedItemCount(mft), cfg.R2.Bucket, feedKey)

	feedURL, err := buildFeedURL(cfg.Feed.BaseURL, token)
	if err != nil {
		return fmt.Errorf("publish: build feed URL: %w", err)
	}
	fmt.Fprintf(out, "feed live at %s\n", feedURL)

	// wa-i1l.10: --prune deletes R2 objects under pg/ that no
	// manifest entry claims. Off by default. Runs LAST so a delete
	// failure doesn't strand the feed pointing at a manifest that
	// references a still-present-but-pruned key. Order:
	//   uploads → manifest → feed → prune
	// If prune fails partway, the run reports the error but the
	// feed + manifest are already consistent; next publish re-
	// computes Stale and prunes whatever didn't get cleaned up.
	if prune && len(plan.Stale) > 0 {
		if err := pruneStaleObjects(ctx, store, plan.Stale, cfg.R2.Bucket, logger, out); err != nil {
			return err
		}
	} else if prune {
		fmt.Fprintln(out, "prune: nothing to delete (0 stale objects on r2)")
	}

	logger.Info("publish complete",
		"uploaded", len(uploads),
		"unchanged", len(plan.Unchanged),
		"stale_on_r2", len(plan.Stale),
		"pruned", prune && len(plan.Stale) > 0)
	return nil
}

// pruneStaleObjects deletes the keys reported in Plan.Stale. Stops
// at the first delete failure so a buggy classification or
// transient R2 issue doesn't cascade through the whole stale list.
// The caller has already printed the per-key delete lines; an
// error mid-loop leaves the surviving stale keys in place for the
// next publish run.
func pruneStaleObjects(
	ctx context.Context,
	store r2.Storage,
	stale []r2.ObjectInfo,
	bucket string,
	logger *slog.Logger,
	out io.Writer,
) error {
	fmt.Fprintf(out, "prune: deleting %d stale R2 object(s):\n", len(stale))
	for _, obj := range stale {
		if err := store.DeleteObject(ctx, obj.Key); err != nil {
			// errors.Is(err, r2.ErrNoSuchKey) on prune is benign
			// (raced with another deleter), but the runtime cost
			// of a few extra lines is small enough that we report
			// it consistently — operator can suppress at log level
			// if it gets noisy.
			logger.Error("prune: delete failed",
				"key", obj.Key, "err", err.Error())
			return fmt.Errorf("publish: prune %s: %w", obj.Key, err)
		}
		fmt.Fprintf(out, "  pruned r2://%s/%s\n", bucket, obj.Key)
	}
	fmt.Fprintf(out, "pruned %d stale R2 object(s)\n", len(stale))
	logger.Info("prune complete", "deleted", len(stale))
	return nil
}

// runPublishFeedOnlyTail handles wa-i1l.8: regenerate pg.xml with
// the current token + manifest contents, PUT it. Skips Diff +
// uploads + manifest Save. Caller already loaded the manifest.
func runPublishFeedOnlyTail(
	ctx context.Context,
	out io.Writer,
	store r2.Storage,
	cfg *model.Config,
	token string,
	mft *model.Manifest,
	logger *slog.Logger,
) error {
	overlayLocalFeedFields(mft, logger)

	feedXML, err := buildFeed(mft, cfg, token)
	if err != nil {
		return fmt.Errorf("publish: feed: %w", err)
	}
	if _, err := store.PutObject(ctx, feedKey,
		bytes.NewReader(feedXML), int64(len(feedXML)), feedContentType); err != nil {
		return fmt.Errorf("publish: put %s: %w", feedKey, err)
	}
	fmt.Fprintf(out, "regenerating pg.xml (%d items) → r2://%s/%s ✓\n",
		feedItemCount(mft), cfg.R2.Bucket, feedKey)

	feedURL, err := buildFeedURL(cfg.Feed.BaseURL, token)
	if err != nil {
		return fmt.Errorf("publish: build feed URL: %w", err)
	}
	fmt.Fprintf(out, "feed live at %s\n", feedURL)
	logger.Info("feed-only complete",
		"items", feedItemCount(mft),
		"feed_key", feedKey)
	return nil
}

// runPublishDryRunTail handles wa-i1l.9: compute the diff, print
// what WOULD be uploaded + the final feed URL, perform NO R2
// writes. Manifest already loaded by caller.
//
// The "would upload" lines mirror the modeDefault upload progress
// format so an operator can A/B the dry-run output against a real
// run and read the same shapes.
func runPublishDryRunTail(
	ctx context.Context,
	out io.Writer,
	store r2.Storage,
	cfg *model.Config,
	mft *model.Manifest,
	logger *slog.Logger,
) error {
	plan, err := publish.Diff(ctx, store, mft)
	if err != nil {
		return fmt.Errorf("publish: diff: %w", err)
	}
	fmt.Fprintln(out, plan.String())

	uploads := append([]model.ManifestEntry{}, plan.ToUpload...)
	uploads = append(uploads, plan.ToOverwrite...)
	for _, entry := range uploads {
		key := entry.R2Key
		if key == "" {
			key = publish.EpisodePrefix + entry.Slug + ".mp3"
		}
		size := dryRunBodySize(entry.Slug)
		fmt.Fprintf(out, "would upload %s.mp3 (%s) → r2://%s/%s\n",
			entry.Slug, size, cfg.R2.Bucket, key)
	}
	if len(plan.Stale) > 0 {
		fmt.Fprintf(out, "stale on r2 (would NOT prune without --prune):\n")
		for _, s := range plan.Stale {
			fmt.Fprintf(out, "  - r2://%s/%s\n", cfg.R2.Bucket, s.Key)
		}
	}
	fmt.Fprintf(out, "would regenerate pg.xml (%d items) → r2://%s/%s\n",
		feedItemCount(mft), cfg.R2.Bucket, feedKey)
	fmt.Fprintln(out, "dry-run: no writes performed")

	logger.Info("dry-run complete",
		"would_upload", len(uploads),
		"unchanged", len(plan.Unchanged),
		"stale_on_r2", len(plan.Stale))
	return nil
}

// dryRunBodySize returns a human-readable size of the cached MP3
// for slug, or "unknown" when the cache file is missing. Dry-run
// shouldn't fail on a missing file — the operator may run dry-run
// before any build has produced cache contents.
func dryRunBodySize(slug string) string {
	info, err := os.Stat(cache.OutPath(slug))
	if err != nil {
		return "size unknown"
	}
	return formatBytes(info.Size())
}

// uploadOneEpisode handles one ToUpload / ToOverwrite entry. Reads
// the MP3 from cache, calls PutWithRetries (which already classifies
// retryable / fatal via the wa-i1l.4 taxonomy), patches the manifest
// entry on success.
func uploadOneEpisode(
	ctx context.Context,
	store r2.Storage,
	mft *model.Manifest,
	entry model.ManifestEntry,
	bucket string,
	now time.Time,
	logger *slog.Logger,
	out io.Writer,
) error {
	srcPath := cache.OutPath(entry.Slug)
	body, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}

	r2Key := entry.R2Key
	if r2Key == "" {
		r2Key = publish.EpisodePrefix + entry.Slug + ".mp3"
	}

	etag, err := r2.PutWithRetries(ctx, store, r2Key, body, mp3ContentType, r2.UploadOpts{Logger: logger})
	if err != nil {
		return err
	}

	// Patch the manifest entry. The map value is a copy; reassign
	// to commit the change back to mft.Entries. PublishedAt is set
	// here, never optimistically — wa-i1l.4 acceptance "set after
	// upload, never before" so the RSS pubDate ordering doesn't
	// promote unpublished essays.
	patched := mft.Entries[entry.Slug]
	patched.R2Key = r2Key
	patched.R2ETag = etag
	pub := now.UTC()
	patched.PublishedAt = &pub
	mft.Entries[entry.Slug] = patched

	fmt.Fprintf(out, "uploading %s.mp3 (%s) → r2://%s/%s ✓\n",
		entry.Slug, formatBytes(int64(len(body))), bucket, r2Key)
	return nil
}

// feedItemCount returns the number of manifest entries eligible to
// appear in the feed (R2Key set + PublishedAt set). Used only for
// the §3 summary line; feed.Generate's own filterEligible is the
// authoritative inclusion test.
func feedItemCount(mft *model.Manifest) int {
	n := 0
	for _, e := range mft.Entries {
		if e.R2Key != "" && e.PublishedAt != nil {
			n++
		}
	}
	return n
}

// overlayLocalFeedFields patches per-essay feed fields (SourceURL,
// Description) on every entry in mft from the cached local manifest
// (~/.cache/wiki-audio/manifest.json), if one exists. The publish
// flow's source-of-truth is R2, but the build pipeline writes the
// locally-derived feed fields to the local manifest first; without
// this overlay, a publish regenerates pg.xml without per-item
// <link>/<description> on entries whose R2-side manifest predates
// wa-bo5.
//
// The overlay is best-effort: if the local manifest is missing,
// unreadable, or lacks an entry for the slug, mft is left unchanged
// for that slug. Only the two wa-bo5 fields are overlaid — the rest
// (R2Key/R2ETag/PublishedAt/etc.) is owned by the publish path.
//
// This is the surgical fix for wa-bo5's bundled re-publish; a fuller
// "merge local entries that R2 doesn't know about yet" sync belongs
// to pane-9's r2/manifest area.
func overlayLocalFeedFields(mft *model.Manifest, logger *slog.Logger) {
	manifestPath := cache.Dir() + "/manifest.json"
	local, err := readLocalManifest(manifestPath)
	if err != nil {
		logger.Warn("overlay: local manifest unreadable; live feed will lack per-item link/description until next build",
			"path", manifestPath, "err", err.Error())
		return
	}
	for slug, r2Entry := range mft.Entries {
		localEntry, ok := local.Entries[slug]
		if !ok {
			continue
		}
		if r2Entry.SourceURL == "" && localEntry.SourceURL != "" {
			r2Entry.SourceURL = localEntry.SourceURL
		}
		if r2Entry.Description == "" && localEntry.Description != "" {
			r2Entry.Description = localEntry.Description
		}
		mft.Entries[slug] = r2Entry
	}
}

// buildFeed materializes the channel struct from FeedConfig, calls
// feed.Generate, then layers feed.StampTokens. Pulled out so
// runPublishCore stays orchestration-only.
func buildFeed(mft *model.Manifest, cfg *model.Config, token string) ([]byte, error) {
	selfLink, err := buildFeedURL(cfg.Feed.BaseURL, token)
	if err != nil {
		return nil, err
	}
	channel := feed.Channel{
		Title:       cfg.Feed.Title,
		Description: cfg.Feed.Description,
		Author:      cfg.Feed.Author,
		OwnerEmail:  cfg.Feed.OwnerEmail,
		Language:    cfg.Feed.Language,
		Link:        cfg.Feed.BaseURL,
		SelfLinkURL: selfLink,
		CoverImage:  cfg.Feed.CoverImageURL,
		PodcastType: feed.PodcastTypeEpisodic,
		Categories:  cfg.Feed.Categories,
		Copyright:   cfg.Feed.Copyright,
	}
	entries := make([]model.ManifestEntry, 0, len(mft.Entries))
	for _, e := range mft.Entries {
		entries = append(entries, e)
	}
	enclosure := func(e model.ManifestEntry) string {
		return strings.TrimRight(cfg.Feed.BaseURL, "/") + "/" + e.R2Key
	}
	xmlBytes, err := feed.Generate(channel, entries, enclosure)
	if err != nil {
		return nil, err
	}
	return feed.StampTokens(xmlBytes, token), nil
}

// buildFeedURL composes the §9.1 token-bearing self-link URL:
// <BaseURL>/pg.xml?t=<urlescaped-token>. Trailing slashes on
// BaseURL are tolerated.
func buildFeedURL(baseURL, token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + feedKey
	q := u.Query()
	q.Set("t", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

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

// publishFlags carries the user-facing flags. Phase F splits
// --feed-only (wa-i1l.8) and --dry-run (wa-i1l.9) into their own
// beads; this file wires the flag plumbing now and dispatches when
// those beads are claimed by routing through the same Run() body.
type publishFlags struct {
	feedOnly bool
	dryRun   bool
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
			"(used after token rotation; full impl is wa-i1l.8)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"compute and print the diff, do NOT upload anything "+
			"(full impl is wa-i1l.9)")
	cmd.MarkFlagsMutuallyExclusive("feed-only", "dry-run")
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
// --feed-only and --dry-run are scaffolded here but their full
// impls live in wa-i1l.8 and wa-i1l.9; this commit returns
// "not yet implemented" for those branches.
func runPublish(cmd *cobra.Command, flags *publishFlags) error {
	if flags.feedOnly {
		return notImplemented("publish --feed-only (wa-i1l.8)")(cmd, nil)
	}
	if flags.dryRun {
		return notImplemented("publish --dry-run (wa-i1l.9)")(cmd, nil)
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

	// Pre-flight: WIKI_AUDIO_ACCESS_TOKEN MUST be present BEFORE we
	// upload anything. Stamping the feed without it would either
	// silently emit unstamped URLs (StampTokens logs a WARN) or
	// require a redo of the whole publish — both worse than
	// failing fast here.
	token := strings.TrimSpace(os.Getenv("WIKI_AUDIO_ACCESS_TOKEN"))
	if token == "" {
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

	return runPublishCore(ctx, cmd.OutOrStdout(), store, cfg, token, time.Now)
}

// runPublishCore is the orchestrator's pure logic, factored out so
// publish_test.go can drive it with an r2.Fake and a frozen `now`.
// nowFn is a closure (not time.Time) so the test can advance the
// clock between subtests if desired.
//
// Errors here propagate out of runPublish and exit cobra
// non-zero. A mid-batch error leaves the manifest in R2 unchanged
// (Save runs only after every upload succeeds), preserving the
// recovery story.
func runPublishCore(
	ctx context.Context,
	out io.Writer,
	store r2.Storage,
	cfg *model.Config,
	token string,
	nowFn func() time.Time,
) error {
	logger := slog.With("phase", "publish", "bucket", cfg.R2.Bucket)

	mft, err := manifest.Load(ctx, store)
	if err != nil {
		return fmt.Errorf("publish: load manifest: %w", err)
	}
	logger.Info("manifest loaded", "entries", len(mft.Entries))

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
	logger.Info("publish complete",
		"uploaded", len(uploads),
		"unchanged", len(plan.Unchanged),
		"stale_on_r2", len(plan.Stale))
	return nil
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

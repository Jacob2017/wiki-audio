package cli

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/Jacob2017/wiki-audio/internal/config"
)

// creditsPerCharMultilingual is the §1.3 / wa-4cw billing rate for the
// eleven_multilingual_v2 model — 2× flash_v2_5's rate. The cost
// subcommand reports both rates side-by-side so the user can shop
// for a tier rather than a model.
const creditsPerCharMultilingual = 1.0

// scaleTierMonthlyDollars is the §1.3 monthly Scale-tier price.
// Scale covers up to roughly 2,000,000 credits per month (the
// scaleTierCredits constant from build.go).
const scaleTierMonthlyDollars = 330

type costFlags struct {
	all bool
}

func newCostCmd() *cobra.Command {
	flags := &costFlags{}
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Estimate ElevenLabs credit cost across both supported models",
		Long: "Cost answers a tier-level question (\"should I subscribe to Pro?\") " +
			"separately from build --dry-run, which answers a per-run question " +
			"(\"will THIS run blow my budget?\"). Same underlying calculation; " +
			"different framing.\n\n" +
			"Without --all, cost reads the published manifest from R2 (whatever " +
			"has already been built). With --all it walks [wiki].source_dir and " +
			"runs the extractor on every essay, including ones not yet built. The " +
			"latter is the right answer when you are deciding whether to subscribe.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.all {
				return runCostAll(cmd)
			}
			return runCostManifest(cmd)
		},
	}
	cmd.Flags().BoolVar(&flags.all, "all", false,
		"scan source_dir + extract every essay (vs. read the published manifest)")
	return cmd
}

// runCostAll walks the configured source directory, runs the §5.1
// extractor on each .md, and prints the §3 three-line report. No
// chunker, no API calls, no file writes. Malformed and read-error
// essays are skipped silently (logged at WARN) and do not contribute
// to the total — same convention as build --dry-run.
func runCostAll(cmd *cobra.Command) error {
	configPath, _ := cmd.Flags().GetString("config")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}

	files, err := listMarkdownFiles(cfg.Wiki.SourceDir)
	if err != nil {
		return fmt.Errorf("cost --all: scan %s: %w", cfg.Wiki.SourceDir, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("cost --all: no .md essays found under %s", cfg.Wiki.SourceDir)
	}

	logger := slog.With("phase", "cost-all", "n_essays_seen", len(files))
	var totalChars, validCount int
	for _, path := range files {
		r := buildOneEssay(path, cfg)
		if r.err != nil {
			logger.Warn("cost: skipping due to extraction error",
				"slug", r.slug, "err", r.err.Error())
			continue
		}
		if r.malformed {
			logger.Warn("cost: skipping malformed essay",
				"slug", r.slug, "reason", r.malformedReason)
			continue
		}
		totalChars += r.charCount
		validCount++
	}

	printCostReport(cmd.OutOrStdout(), validCount, totalChars)
	return nil
}

// runCostManifest reports cost from the published manifest in R2 —
// i.e. essays that have already been built. Until the r2 manifest
// fetch lands (depends on internal/r2/ being functional), this prints
// the documented "manifest empty" message and exits 0 so the command
// is at least informative on first run.
func runCostManifest(cmd *cobra.Command) error {
	fmt.Fprintln(cmd.OutOrStdout(), "manifest empty — run `wiki-audio cost --all` to estimate from source")
	return nil
}

// printCostReport emits the §3 three-line cost report. Spacing after
// "flash_v2_5" (two spaces) and "multilingual_v2" (one space) matches
// the §3 sample byte-for-byte; the alignment is cosmetic and load-
// bearing for parity with §3, not derived from column math.
func printCostReport(w io.Writer, nEssays, totalChars int) {
	flashCredits := int(float64(totalChars) * creditsPerCharFlash)
	mvCredits := int(float64(totalChars) * creditsPerCharMultilingual)

	fmt.Fprintf(w, "cleaned chars across %d essays: %s\n",
		nEssays, formatThousands(totalChars))
	fmt.Fprintf(w, "flash_v2_5  (0.5 credits/char): %s credits → %s\n",
		formatThousands(flashCredits), tierMessage(flashCredits))
	fmt.Fprintf(w, "multilingual_v2 (1 credit/char): %s credits → %s\n",
		formatThousands(mvCredits), tierMessage(mvCredits))
}

// tierMessage maps a credit volume to the subscription guidance a
// would-be subscriber needs. Boundaries:
//
//	   ≤ 500k → fits Pro $99/mo
//	500k–1M → Scale $330/mo (1 month) or Pro $99 × 2
//	  1M–2M → Scale $330/mo (1 month) or Pro $99 × N (computed)
//	    > 2M → Scale $330/mo × N months or Enterprise quote
//
// The §3 multilingual_v2 sample (880,344 credits) exercises the
// 500k–1M bucket and emits "Scale $330/mo (1 month) or Pro $99 × 2".
func tierMessage(credits int) string {
	switch {
	case credits <= proTierCredits:
		return fmt.Sprintf("fits Pro $%d/mo", proTierDollars)
	case credits <= scaleTierCredits:
		proMonths := ceilDiv(credits, proTierCredits)
		return fmt.Sprintf("Scale $%d/mo (1 month) or Pro $%d × %d",
			scaleTierMonthlyDollars, proTierDollars, proMonths)
	default:
		scaleMonths := ceilDiv(credits, scaleTierCredits)
		return fmt.Sprintf("Scale $%d/mo × %d months or Enterprise quote",
			scaleTierMonthlyDollars, scaleMonths)
	}
}

func ceilDiv(n, d int) int {
	if d == 0 {
		return 0
	}
	return (n + d - 1) / d
}

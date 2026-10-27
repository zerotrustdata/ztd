// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthoncmd

import (
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/autochthon"
)

const (
	defaultStatusGenerations = 10
	defaultStatusSurprises   = 10
	statusTimeLayout         = "2006-01-02 15:04:05"
)

func statusCmd() *cobra.Command {
	return statusCmdWithConfig(shared.NewFileSvc)
}

func statusCmdWithConfig(newFileSvc fileSvcFactory) *cobra.Command {
	var workingDir, anchor string
	var opts autochthon.StatusOptions

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Verify the autochthon tally and report the culture's skill",
		Long: `Verify the tally hash chain under .ztd/autochthon/ from genesis and report the culture without writing anything. It is safe to run beside a running autochthon; a last line still being written is reported as pending and not verified.

Skill is the champion's fit minus the persistence fit on the same samples: positive means the culture predicts this host better than assuming nothing changes. A surprise is a sample whose champion fit is below ` + fmt.Sprint(autochthon.SurpriseFit) + `.

The Anchor row is the verified head as <lines>:<hash>. Record it off the host; --anchor <lines>:<hash> later fails unless line <lines> of the verified chain still has that hash. That catches whole trailing lines removed, which the chain alone cannot. A pending line never satisfies an anchor.

A broken chain, a missing anchor, or a culture.bin from another format is an error. --json emits the full report.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseFlags(cmd, statusFlags); err != nil {
				return err
			}
			if cmd.Flags().Changed("anchor") {
				a, err := autochthon.ParseAnchor(anchor)
				if err != nil {
					return err
				}
				opts.Anchor = &a
			}
			files, err := newFileSvc(workingDir, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			st, err := autochthon.ReadStatus(cmd.Context(), files, opts)
			if err != nil {
				return err
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), st)
			}
			return writeStatus(cmd.OutOrStdout(), st, time.Now())
		},
	}

	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Runtime root that holds .ztd/autochthon/")
	cmd.Flags().IntVar(&opts.Generations, "generations", defaultStatusGenerations, "Most recent generations to report")
	cmd.Flags().IntVar(&opts.Surprises, "surprises", defaultStatusSurprises, "Most recent surprises to report")
	cmd.Flags().StringVar(&anchor, "anchor", "", "Fail unless the verified chain contains this <lines>:<hash> head")

	return cmd
}

func writeStatus(w io.Writer, st *autochthon.Status, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Autochthon\t%s\n", st.Dir)

	tally := fmt.Sprintf("%d lines, chain verified", st.Tally.Lines)
	if st.Tally.Lines == 0 {
		tally = "empty"
	} else {
		tally += ", head " + st.Tally.Head[:12]
	}
	if st.Tally.Pending {
		tally += ", 1 line pending"
	}
	fmt.Fprintf(tw, "Tally\t%s\n", tally)
	if st.Tally.First != nil {
		fmt.Fprintf(tw, "\t%s to %s (last sample %s ago)\n",
			st.Tally.First.Local().Format(statusTimeLayout), st.Tally.Last.Local().Format(statusTimeLayout),
			now.Sub(*st.Tally.Last).Round(time.Second))
	}
	if st.Tally.Lines > 0 {
		fmt.Fprintf(tw, "Anchor\t%s\n", autochthon.Anchor{Lines: st.Tally.Lines, Head: st.Tally.Head})
	}
	if st.Anchor != nil {
		fmt.Fprintf(tw, "\t%s holds\n", st.Anchor.Anchor)
	}

	var culture string
	switch {
	case st.Culture.Present:
		culture = fmt.Sprintf("generation %d, champion %s", st.Culture.Generation, st.Culture.Champion[:12])
		if st.Culture.Persistence {
			culture += " (persistence)"
		}
	case st.Tally.Lines > 0:
		culture = "first generation in progress"
	default:
		culture = "none"
	}
	fmt.Fprintf(tw, "Culture\t%s\n", culture)
	if st.Tally.Scored > 0 {
		fmt.Fprintf(tw, "Skill\t%+.4f (fit %.4f, persistence %.4f, %d samples)\n",
			st.Tally.Fit-st.Tally.Base, st.Tally.Fit, st.Tally.Base, st.Tally.Scored)
		fmt.Fprintf(tw, "Surprises\t%d (fit below %v)\n", st.Tally.Surprises, autochthon.SurpriseFit)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(st.Generations) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "GEN\tSAMPLES\tFIT\tPERSISTENCE\tSKILL")
		for _, g := range st.Generations {
			fmt.Fprintf(tw, "%d\t%d\t%.4f\t%.4f\t%+.4f\n", g.Gen, g.Samples, g.Fit, g.Base, g.Fit-g.Base)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(st.Surprises) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SEQ\tAT\tLOAD\tMEM\tPROCS\tFIT\tPERSISTENCE")
		for _, s := range st.Surprises {
			fmt.Fprintf(tw, "%d\t%s\t%.3f\t%.3f\t%.3f\t%.4f\t%.4f\n",
				s.Seq, s.At.Local().Format(statusTimeLayout), s.Load, s.Mem, s.Procs, s.Fit, s.Base)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

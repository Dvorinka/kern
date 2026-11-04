package orchestrator

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

const (
	maxSplitExplicit = 32
	maxSplitAuto     = 16
	maxSplitJobs     = 256
)

// splitWidth chooses how many copies one matrix member becomes.
// requested > 0 is an explicit count. requested <= 0 sizes from available CPUs.
// siblings is the matrix width; the product stays within maxSplitJobs.
func splitWidth(requested, available, siblings int) int {
	n := requested
	if requested <= 0 {
		n = available
		if n < 1 {
			n = 1
		}
		if n > maxSplitAuto {
			n = maxSplitAuto
		}
	} else {
		if n > maxSplitExplicit {
			n = maxSplitExplicit
		}
	}
	if siblings < 1 {
		siblings = 1
	}
	if limit := maxSplitJobs / siblings; n > limit {
		n = limit
	}
	if n < 1 {
		n = 1
	}
	return n
}

// expandSplits turns each opted-in seed into N jobs that share JobKey.
// Width 1 leaves the seed alone. width reports an error from fleet lookup.
// shortcut: shards are whole jobs with their own checkout. The script must
// partition on KERN_SHARD; KERN does not rewrite commands or share a workspace.
func expandSplits(seeds []store.JobSeed, width func(store.JobSeed, int) (int, error)) ([]store.JobSeed, error) {
	counts := map[string]int{}
	for _, s := range seeds {
		counts[s.JobKey]++
	}
	out := make([]store.JobSeed, 0, len(seeds))
	for _, s := range seeds {
		if s.SplitN == 0 && !s.SplitAuto {
			out = append(out, s)
			continue
		}
		n, err := width(s, counts[s.JobKey])
		if err != nil {
			return nil, err
		}
		s.SplitN, s.SplitAuto = 0, false
		if n < 2 {
			out = append(out, s)
			continue
		}
		for i := 1; i <= n; i++ {
			shard := s
			shard.Key = s.Key + " (shard " + strconv.Itoa(i) + "/" + strconv.Itoa(n) + ")"
			shard.Name = s.Name + " (shard " + strconv.Itoa(i) + "/" + strconv.Itoa(n) + ")"
			shard.Matrix = copyAny(s.Matrix)
			shard.Matrix["shard"] = i
			shard.Env = copyEnv(s.Env)
			shard.Env["KERN_SHARD"] = strconv.Itoa(i)
			shard.Env["KERN_SHARDS"] = strconv.Itoa(n)
			shard.Steps = append([]store.StepSeed(nil), s.Steps...)
			out = append(out, shard)
		}
	}
	return out, nil
}

func copyAny(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// expandRunSplits fans opted-in jobs across the fleet. auto reads live runner
// CPUs, which compile time does not know.
func (o *Orchestrator) expandRunSplits(ctx context.Context, projectID uuid.UUID, seeds []store.JobSeed) ([]store.JobSeed, error) {
	auto := false
	for _, s := range seeds {
		if s.SplitAuto {
			auto = true
			break
		}
	}
	var orgID uuid.UUID
	if auto {
		proj, err := o.Store.ProjectByID(ctx, projectID)
		if err != nil {
			return nil, err
		}
		orgID = proj.OrgID
	}
	cache := map[string]int{}
	return expandSplits(seeds, func(s store.JobSeed, siblings int) (int, error) {
		available := 0
		requested := s.SplitN
		if s.SplitAuto {
			requested = 0
			key := strings.Join(s.RunsOn, "\x00")
			if n, ok := cache[key]; ok {
				available = n
			} else {
				n, err := o.Store.FleetSlots(ctx, orgID, s.RunsOn)
				if err != nil {
					return 0, fmt.Errorf("fleet slots: %w", err)
				}
				cache[key] = n
				available = n
			}
		}
		return splitWidth(requested, available, siblings), nil
	})
}

func copyEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

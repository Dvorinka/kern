package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// cmdSplit partitions the files matching a glob into N stable hash buckets and
// prints strategy.split. Each shard is a full job that sees KERN_SHARD (1-based)
// and KERN_SHARDS; the script partitions the work.
func (c *cli) cmdSplit(args []string) error {
	opts, _ := parseFlags(args, "pattern", "shards", "root")
	if opts["pattern"] == "" {
		return fmt.Errorf("--pattern is required (e.g. --pattern '**/*_test.go')")
	}
	shards := atoi(opts["shards"])
	if shards < 2 {
		return fmt.Errorf("--shards must be >= 2")
	}
	root := opts["root"]
	if root == "" {
		root = "."
	}
	matches, err := doublestar.FilepathGlob(root + "/" + opts["pattern"])
	if err != nil {
		return fmt.Errorf("bad pattern: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no files matched %s under %s", opts["pattern"], root)
	}
	buckets := make([][]string, shards)
	for _, m := range matches {
		rel := strings.TrimPrefix(m, root+"/")
		h := fnv.New32a()
		_, _ = h.Write([]byte(rel))
		buckets[int(h.Sum32()%uint32(shards))] = append(buckets[int(h.Sum32()%uint32(shards))], rel)
	}
	indices := make([]int, shards)
	for i := range indices {
		indices[i] = i + 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "strategy:\n  split: %d\n", shards)
	b.WriteString("# Each copy gets KERN_SHARD (1-based) and KERN_SHARDS. Partition the work on those.\n")
	fmt.Fprintf(&b, "# %d files across %d shards:\n", len(matches), shards)
	for i, files := range buckets {
		sort.Strings(files)
		fmt.Fprintf(&b, "#   shard %d: %d files\n", i+1, len(files))
	}
	if c.json {
		printJSON(map[string]any{
			"split":  shards,
			"env":    []string{"KERN_SHARD", "KERN_SHARDS"},
			"shards": indices,
			"buckets": func() map[string][]string {
				m := map[string][]string{}
				for i, f := range buckets {
					m[fmt.Sprintf("%d", i+1)] = f
				}
				return m
			}(),
		})
		return nil
	}
	fmt.Print(b.String())
	fmt.Println("\nShard contents:")
	for i, files := range buckets {
		fmt.Printf("  shard %d (%d):\n", i+1, len(files))
		for _, f := range files {
			fmt.Println("   ", f)
		}
	}
	return nil
}

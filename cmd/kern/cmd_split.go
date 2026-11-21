package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// cmdSplit partitions the files matching a glob into N stable hash buckets and
// prints strategy.split. Each shard is a full job that sees KERN_SHARD (1-based)
// and KERN_SHARDS; the script partitions the work.
func (c *cli) cmdSplit(args []string) error {
	opts, _ := parseFlags(args, "pattern", "shards", "root", "timings")
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
	rels := make([]string, 0, len(matches))
	for _, m := range matches {
		rels = append(rels, strings.TrimPrefix(m, root+"/"))
	}
	buckets := make([][]string, shards)
	if opts["timings"] != "" {
		durs, err := loadTimings(opts["timings"], rels, root)
		if err != nil {
			return err
		}
		buckets = lptBuckets(rels, durs, shards)
	} else {
		for _, rel := range rels {
			h := fnv.New32a()
			_, _ = h.Write([]byte(rel))
			buckets[int(h.Sum32()%uint32(shards))] = append(buckets[int(h.Sum32()%uint32(shards))], rel)
		}
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

// lptBuckets assigns files to shards by Longest-Processing-Time greedy
// (#376): sort by duration desc, place each on the lightest shard. Untimed
// files count as zero — they spread over whichever shard is lightest at the
// end, which is the correct greedy outcome.
func lptBuckets(files []string, durations map[string]float64, shards int) [][]string {
	type item struct {
		file string
		dur  float64
	}
	items := make([]item, 0, len(files))
	for _, f := range files {
		items = append(items, item{f, durations[f]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].dur > items[j].dur })
	buckets := make([][]string, shards)
	load := make([]float64, shards)
	for _, it := range items {
		light := 0
		for s := 1; s < shards; s++ {
			if load[s] < load[light] {
				light = s
			}
		}
		buckets[light] = append(buckets[light], it.file)
		load[light] += it.dur
	}
	return buckets
}

// loadTimings reads junit XML, playwright JSON, or `go test -json` output
// and maps durations onto the matched files. Unmatched timing entries are
// ignored; files with no timing get duration 0 (LPT handles the rest).
func loadTimings(path string, files []string, root string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	isXML := bytes.HasPrefix(bytes.TrimSpace(data), []byte("<"))
	isDoc := bytes.HasPrefix(bytes.TrimSpace(data), []byte("{"))
	// NDJSON (go test -json) also starts with '{' — distinguish by whether a
	// playwright-style suites tree actually unmarshals.
	if isDoc {
		var probe struct {
			Suites []json.RawMessage `json:"suites"`
		}
		if json.Unmarshal(data, &probe) != nil || len(probe.Suites) == 0 {
			isDoc = false
		}
	}
	switch {
	case isXML:
		type testcase struct {
			Classname string  `xml:"classname,attr"`
			File      string  `xml:"file,attr"`
			Time      float64 `xml:"time,attr"`
		}
		type suite struct {
			Cases []testcase `xml:"testcase"`
		}
		type root struct {
			Suites []suite `xml:"testsuite"`
			Cases  []testcase `xml:"testcase"`
		}
		var doc root
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("junit timings: %w", err)
		}
		for _, tc := range append(doc.Cases, func() []testcase {
			var all []testcase
			for _, s := range doc.Suites {
				all = append(all, s.Cases...)
			}
			return all
		}()...) {
			if tc.File != "" {
				out[normalizeTimingPath(tc.File)] += tc.Time
				continue
			}
			if p := classnamePath(tc.Classname); p != "" {
				out[p] += tc.Time
			}
		}
	case isDoc:
		// playwright report JSON: {suites:[{suites/specs:[{file, tests:[{results:[{duration}]}]}]}]}
		type result struct {
			Duration float64 `json:"duration"`
		}
		type test struct {
			Results []result `json:"results"`
		}
		type spec struct {
			File  string `json:"file"`
			Tests []test `json:"tests"`
		}
		type suite struct {
			Suites []suite `json:"suites"`
			Specs  []spec  `json:"specs"`
		}
		var doc struct {
			Suites []suite `json:"suites"`
		}
		if err := json.Unmarshal(data, &doc); err == nil && len(doc.Suites) > 0 {
			var walk func(s suite)
			walk = func(s suite) {
				for _, sp := range s.Specs {
					for _, t := range sp.Tests {
						for _, r := range t.Results {
							out[normalizeTimingPath(sp.File)] += r.Duration / 1000
						}
					}
				}
				for _, sub := range s.Suites {
					walk(sub)
				}
			}
			for _, s := range doc.Suites {
				walk(s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("unrecognized JSON timings format")
	default:
		// go test -json: one event object per line.
		testNames := map[string]bool{}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var ev struct {
				Action  string  `json:"Action"`
				Test    string  `json:"Test"`
				Elapsed float64 `json:"Elapsed"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" || ev.Action != "pass" {
				continue
			}
			testNames[ev.Test] = true
		}
		// Map each test func to the file that declares it.
		for _, f := range files {
			content, err := os.ReadFile(filepath.Join(root, f))
			if err != nil {
				continue
			}
			for name := range testNames {
				if bytes.Contains(content, []byte("func "+name+"(")) {
					out[f] += goTestElapsed(data, name)
				}
			}
		}
	}
	// Re-key junit/playwright paths onto matched files: timings often carry
	// workspace-relative paths while the glob was repo-relative.
	if len(out) > 0 {
		resolved := map[string]float64{}
		for _, f := range files {
			for p, d := range out {
				if p == f || strings.HasSuffix(p, "/"+f) || strings.HasSuffix(f, "/"+p) {
					resolved[f] += d
				}
			}
		}
		return resolved, nil
	}
	return out, nil
}

// goTestElapsed sums Elapsed for one test across go test -json events.
func goTestElapsed(data []byte, name string) float64 {
	var total float64
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var ev struct {
			Action  string  `json:"Action"`
			Test    string  `json:"Test"`
			Elapsed float64 `json:"Elapsed"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Test == name && ev.Action == "pass" {
			total += ev.Elapsed
		}
	}
	return total
}

// normalizeTimingPath strips leading ./ and anchors to forward slashes.
func normalizeTimingPath(p string) string {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	return p
}

// classnamePath turns a junit classname (pytest dots, java packages) into a
// candidate repo-relative path stem.
func classnamePath(cls string) string {
	cls = strings.TrimSpace(strings.Split(cls, "::")[0])
	if cls == "" {
		return ""
	}
	return strings.ReplaceAll(cls, ".", "/")
}

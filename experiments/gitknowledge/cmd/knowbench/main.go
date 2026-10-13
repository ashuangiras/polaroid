// Command knowbench measures both knowledge stores on equal datasets.
//
//	go run ./cmd/knowbench -sizes 100,1000,10000 -runs 3 -out bench.json
//
// Each (adapter, size, run) is a separate process, so its peak RSS is its own.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	git "github.com/go-git/go-git/v5"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/gitstore"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/sqlitestore"
)

type stats struct {
	N                           int
	Median, P95, P99, Max, Mean float64 // milliseconds
}

func summarize(d []time.Duration) stats {
	if len(d) == 0 {
		return stats{}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	ms := func(x time.Duration) float64 { return float64(x.Nanoseconds()) / 1e6 }
	q := func(p float64) float64 { return ms(d[min(len(d)-1, int(p*float64(len(d))))]) }
	var sum time.Duration
	for _, x := range d {
		sum += x
	}
	return stats{N: len(d), Median: q(0.5), P95: q(0.95), P99: q(0.99), Max: ms(d[len(d)-1]), Mean: ms(sum / time.Duration(len(d)))}
}

type result struct {
	Adapter, Note       string
	Size, Run           int
	InitialPublishMs    float64
	ReadActive          stats
	ReadCold            stats
	ReadAtOld           stats
	PublishOne          stats
	PublishMulti10      stats
	ResolveNested11     stats
	ConflictDetect      stats
	RestartFirstReadMs  float64
	StorageBytes        int64
	StorageFiles        int
	StorageRepackBytes  int64 `json:",omitempty"`
	HeapInuseBytes      uint64
	MaxRSSBytes         int64
	TransferInitialMs   float64
	TransferInitialB    int64
	TransferIncrMs      float64 `json:",omitempty"`
	TransferIncrB       int64   `json:",omitempty"`
	TransferIncrObjects int     `json:",omitempty"`
}

func main() {
	single := flag.Bool("case", false, "run one case (internal)")
	adapter := flag.String("adapter", "git", "git or sqlite")
	n := flag.Int("n", 100, "procedures")
	run := flag.Int("run", 1, "run number")
	sizes := flag.String("sizes", "100,1000,10000", "dataset sizes")
	runs := flag.Int("runs", 3, "runs per case")
	out := flag.String("out", "", "write all results as JSON here")
	variants := flag.String("adapters", "sqlite,git,git-pack", "adapters to measure; add -nosync to measure without fsync")
	flag.Parse()
	if *single {
		if p := os.Getenv("KNOWBENCH_CPUPROFILE"); p != "" {
			f, _ := os.Create(p)
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
		r, err := measure(*adapter, *n, *run)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	var all []result
	for _, s := range strings.Split(*sizes, ",") {
		size, _ := strconv.Atoi(s)
		for _, a := range strings.Split(*variants, ",") {
			for r := 1; r <= *runs; r++ {
				cmd := exec.Command(os.Args[0], "-case", "-adapter", a, "-n", s, "-run", strconv.Itoa(r))
				cmd.Stderr = os.Stderr
				b, err := cmd.Output()
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s n=%d run %d: %v\n", a, size, r, err)
					os.Exit(1)
				}
				var res result
				if err := json.Unmarshal(b, &res); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				all = append(all, res)
				fmt.Fprintf(os.Stderr, "done %s n=%d run %d\n", a, size, r)
			}
		}
	}
	if *out != "" {
		b, _ := json.MarshalIndent(all, "", "  ")
		os.WriteFile(*out, append(b, '\n'), 0o644)
	}
	table(all)
}

const guidance = "agent.guidance.shared"

// dataset: the guidance plus groups of ten; in each group p0 composes
// p1 and p2, which compose p3..p6, which compose p7..p9: eleven nodes
// (with the guidance) to a resolution, depth four.
var children = map[int][]int{0: {1, 2}, 1: {3, 4}, 2: {5, 6}, 3: {7}, 4: {8}, 5: {9}}

func key(g, j int) string { return fmt.Sprintf("kb.t%d.g%d.p%d", g/100, g%100, j) }

func doc(k string, refs []knowledge.Reference, rev int) *knowledge.Procedure {
	steps := make([]map[string]string, 6)
	for i := range steps {
		steps[i] = map[string]string{"id": fmt.Sprintf("s%d", i), "action": fmt.Sprintf("Step %d of %s, revision %d: run the documented command for the module, read its output, and stop on the first failure.", i, k, rev)}
	}
	instr, _ := json.Marshal(map[string]any{"steps": steps})
	contract, _ := json.Marshal(map[string]any{"preconditions": "The module builds with the pinned toolchain.", "expected_outcome": "Every step exits 0.",
		"verification": "Each command's exit status and a short excerpt of its output.", "boundaries": "Changes nothing outside the module."})
	inputs := []string{"module"}
	if k == guidance {
		inputs = nil
	}
	return &knowledge.Procedure{Key: k, Goal: "Procedure " + k, Method: "Run the steps in order; decide each reference before following it.", Inputs: inputs,
		Contract: contract, Instructions: instr, References: refs}
}

func dataset(n int) []knowledge.Change {
	guide := knowledge.Reference{Name: "guidance", Key: guidance}
	out := []knowledge.Change{{Key: guidance, Procedure: doc(guidance, nil, 0)}}
	for i := 0; len(out) < n; i++ {
		g, j := i/10, i%10
		refs := []knowledge.Reference{guide}
		if len(out)+10-j <= n { // only compose within complete groups
			for _, c := range children[j] {
				refs = append(refs, knowledge.Reference{Name: fmt.Sprintf("c%d", c), Key: key(g, c), Inputs: map[string]string{"module": "module"}})
			}
		}
		out = append(out, knowledge.Change{Key: key(g, j), Procedure: doc(key(g, j), refs, 0)})
	}
	return out
}

type opener func(dir string, trusted ...string) (knowledge.Store, error)

func openerFor(adapter string) opener {
	noSync := strings.HasSuffix(adapter, "-nosync")
	if strings.HasPrefix(adapter, "sqlite") {
		return func(dir string, _ ...string) (knowledge.Store, error) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			return sqlitestore.Open(filepath.Join(dir, "knowledge.db"), knowledge.DefaultPolicy, noSync)
		}
	}
	return func(dir string, trusted ...string) (knowledge.Store, error) {
		return gitstore.Open(dir, gitstore.Options{Policy: knowledge.DefaultPolicy, TrustedRemotes: trusted, UnsafeNoSync: noSync,
			PackPerPublish: strings.HasPrefix(adapter, "git-pack")})
	}
}

func measure(adapter string, n, run int) (result, error) {
	ctx := context.Background()
	open := openerFor(adapter)
	root, err := os.MkdirTemp("", "knowbench-")
	if err != nil {
		return result{}, err
	}
	defer os.RemoveAll(root)
	dir := filepath.Join(root, "a")
	s, err := open(dir)
	if err != nil {
		return result{}, err
	}
	res := result{Adapter: adapter, Size: n, Run: run}
	data := dataset(n)
	rng := rand.New(rand.NewPCG(uint64(n), uint64(run)))
	note := knowledge.Note{Author: "bench"}

	t0 := time.Now()
	snap0, err := s.Publish(ctx, "", data, note)
	if err != nil {
		return res, fmt.Errorf("initial publish: %w", err)
	}
	res.InitialPublishMs = ms(time.Since(t0))
	head := snap0
	keys := make([]string, len(data))
	for i, c := range data {
		keys[i] = c.Key
	}
	others := keys[1:]
	byKey := map[string]int{}
	for i, c := range data {
		byKey[c.Key] = i
	}
	editFast := func(k string, rev int) knowledge.Change {
		p := *data[byKey[k]].Procedure
		p.Goal = fmt.Sprintf("Procedure %s, revision %d", k, rev)
		return knowledge.Change{Key: k, Procedure: &p}
	}

	var d []time.Duration
	for range 30 {
		t := time.Now()
		if head, err = s.Publish(ctx, head, []knowledge.Change{editFast(others[rng.IntN(len(others))], rng.IntN(1<<30))}, note); err != nil {
			return res, fmt.Errorf("publish one: %w", err)
		}
		d = append(d, time.Since(t))
	}
	res.PublishOne = summarize(d)

	d = nil
	for range 10 {
		var ch []knowledge.Change
		seen := map[string]bool{}
		for len(ch) < min(10, len(others)) {
			k := others[rng.IntN(len(others))]
			if !seen[k] {
				seen[k] = true
				ch = append(ch, editFast(k, rng.IntN(1<<30)))
			}
		}
		t := time.Now()
		if head, err = s.Publish(ctx, head, ch, note); err != nil {
			return res, fmt.Errorf("publish multi: %w", err)
		}
		d = append(d, time.Since(t))
	}
	res.PublishMulti10 = summarize(d)

	readLoop := func(read func(k string) error) (stats, error) {
		var d []time.Duration
		for range 2000 {
			k := keys[rng.IntN(len(keys))]
			t := time.Now()
			if err := read(k); err != nil {
				return stats{}, err
			}
			d = append(d, time.Since(t))
		}
		return summarize(d), nil
	}
	if res.ReadActive, err = readLoop(func(k string) error { _, err := s.ReadActive(ctx, k); return err }); err != nil {
		return res, err
	}
	if res.ReadAtOld, err = readLoop(func(k string) error { _, err := s.ReadAt(ctx, k, snap0); return err }); err != nil {
		return res, err
	}
	groups := (n - 1) / 10
	d = nil
	for range 300 {
		k := guidance
		if groups > 0 {
			k = key(rng.IntN(groups), 0)
		}
		t := time.Now()
		node, err := knowledge.Resolve(ctx, s, k, head)
		if err != nil {
			return res, err
		}
		d = append(d, time.Since(t))
		_ = node
	}
	res.ResolveNested11 = summarize(d)

	d = nil
	for range 30 {
		t := time.Now()
		if _, err := s.Publish(ctx, snap0, []knowledge.Change{editFast(others[0], 1)}, note); !errors.Is(err, knowledge.ErrConflict) {
			return res, fmt.Errorf("stale publish: %v", err)
		}
		d = append(d, time.Since(t))
	}
	res.ConflictDetect = summarize(d)

	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	res.HeapInuseBytes = m.HeapInuse

	s.Close()
	var restarts []time.Duration
	for range 5 {
		t := time.Now()
		if s, err = open(dir); err != nil {
			return res, err
		}
		if _, err := s.ReadActive(ctx, keys[len(keys)-1]); err != nil {
			return res, err
		}
		restarts = append(restarts, time.Since(t))
		s.Close()
	}
	res.RestartFirstReadMs = summarize(restarts).Median
	if s, err = open(dir); err != nil {
		return res, err
	}
	d = nil
	for _, i := range rng.Perm(len(keys))[:min(200, len(keys))] {
		t := time.Now()
		if _, err := s.ReadActive(ctx, keys[i]); err != nil {
			return res, err
		}
		d = append(d, time.Since(t))
	}
	res.ReadCold = summarize(d)
	s.Close()
	res.StorageBytes, res.StorageFiles = du(dir)

	switch strings.TrimSuffix(strings.TrimSuffix(adapter, "-nosync"), "-pack") {
	case "sqlite":
		res.Note = "transfer = VACUUM INTO copy + open; no incremental mechanism exists"
		s, _ = open(dir)
		t := time.Now()
		dst := filepath.Join(root, "copy.db")
		if err := s.(*sqlitestore.Store).Backup(ctx, dst); err != nil {
			return res, err
		}
		c, err := sqlitestore.Open(dst, knowledge.DefaultPolicy)
		if err != nil {
			return res, err
		}
		if _, err := c.ReadActive(ctx, keys[0]); err != nil {
			return res, err
		}
		res.TransferInitialMs = ms(time.Since(t))
		fi, _ := os.Stat(dst)
		res.TransferInitialB = fi.Size()
		c.Close()
		s.Close()
	case "git":
		s, _ = open(dir)
		a := s.(*gitstore.Store)
		bs, err := open(filepath.Join(root, "b"), "a")
		if err != nil {
			return res, err
		}
		b := bs.(*gitstore.Store)
		t := time.Now()
		in, err := b.Fetch(ctx, "a", gitstore.LocalURL(dir))
		if err == nil {
			_, err = b.Accept(ctx, in)
		}
		if err != nil {
			return res, fmt.Errorf("initial transfer: %w", err)
		}
		res.TransferInitialMs, res.TransferInitialB = ms(time.Since(t)), in.Bytes
		if head, err = a.Publish(ctx, head, []knowledge.Change{editFast(others[0], 7)}, note); err != nil {
			return res, err
		}
		t = time.Now()
		in, err = b.Fetch(ctx, "a", gitstore.LocalURL(dir))
		if err == nil {
			_, err = b.Accept(ctx, in)
		}
		if err != nil {
			return res, fmt.Errorf("incremental transfer: %w", err)
		}
		res.TransferIncrMs, res.TransferIncrB, res.TransferIncrObjects = ms(time.Since(t)), in.Bytes, in.Objects
		b.Close()
		a.Close()
		repo, err := git.PlainOpen(dir)
		if err == nil {
			err = repo.RepackObjects(&git.RepackConfig{})
		}
		if err != nil {
			res.Note = "repack failed: " + err.Error()
		} else {
			res.StorageRepackBytes, _ = du(dir)
		}
	}
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	res.MaxRSSBytes = ru.Maxrss // bytes on darwin, KiB on linux
	if runtime.GOOS == "linux" {
		res.MaxRSSBytes *= 1024
	}
	return res, nil
}

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

func du(dir string) (int64, int) {
	var total int64
	files := 0
	filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				total += fi.Size()
				files++
			}
		}
		return nil
	})
	return total, files
}

// table prints the median over runs of each metric.
func table(all []result) {
	type k struct {
		a string
		n int
	}
	groups := map[k][]result{}
	var order []k
	for _, r := range all {
		kk := k{r.Adapter, r.Size}
		if groups[kk] == nil {
			order = append(order, kk)
		}
		groups[kk] = append(groups[kk], r)
	}
	med := func(rs []result, f func(result) float64) float64 {
		v := make([]float64, len(rs))
		for i, r := range rs {
			v[i] = f(r)
		}
		sort.Float64s(v)
		return v[len(v)/2]
	}
	fmt.Println("| adapter | n | initial publish ms | read active p50/p99 ms | read old p50/p99 | publish 1 p50/p99 | publish 10 p50/p99 | resolve 11 p50/p99 | conflict p50 | restart+read ms | storage KiB (files) | repacked KiB | heap MiB | max RSS MiB | transfer initial ms/KiB | transfer incr ms/B (objects) |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, kk := range order {
		rs := groups[kk]
		p := func(f func(result) stats) string {
			return fmt.Sprintf("%.3f / %.3f", med(rs, func(r result) float64 { return f(r).Median }), med(rs, func(r result) float64 { return f(r).P99 }))
		}
		incr := "n/a"
		if strings.HasPrefix(kk.a, "git") {
			incr = fmt.Sprintf("%.1f / %.0f (%.0f)", med(rs, func(r result) float64 { return r.TransferIncrMs }), med(rs, func(r result) float64 { return float64(r.TransferIncrB) }),
				med(rs, func(r result) float64 { return float64(r.TransferIncrObjects) }))
		}
		repack := "-"
		if strings.HasPrefix(kk.a, "git") {
			repack = fmt.Sprintf("%.0f", med(rs, func(r result) float64 { return float64(r.StorageRepackBytes) / 1024 }))
		}
		fmt.Printf("| %s | %d | %.0f | %s | %s | %s | %s | %s | %.3f | %.1f | %.0f (%.0f) | %s | %.1f | %.1f | %.0f / %.0f | %s |\n",
			kk.a, kk.n, med(rs, func(r result) float64 { return r.InitialPublishMs }),
			p(func(r result) stats { return r.ReadActive }), p(func(r result) stats { return r.ReadAtOld }),
			p(func(r result) stats { return r.PublishOne }), p(func(r result) stats { return r.PublishMulti10 }),
			p(func(r result) stats { return r.ResolveNested11 }), med(rs, func(r result) float64 { return r.ConflictDetect.Median }),
			med(rs, func(r result) float64 { return r.RestartFirstReadMs }),
			med(rs, func(r result) float64 { return float64(r.StorageBytes) / 1024 }), med(rs, func(r result) float64 { return float64(r.StorageFiles) }),
			repack, med(rs, func(r result) float64 { return float64(r.HeapInuseBytes) / (1 << 20) }),
			med(rs, func(r result) float64 { return float64(r.MaxRSSBytes) / (1 << 20) }),
			med(rs, func(r result) float64 { return r.TransferInitialMs }), med(rs, func(r result) float64 { return float64(r.TransferInitialB) / 1024 }), incr)
	}
}

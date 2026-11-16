// runtime_model.go - learn how long each task takes on each CPU platform
// (supervised regression), validate it, and write the model for the scheduler.
//
// Build: go build -o bin/runtime_model ml/runtime_model.go
// Run:   ./bin/runtime_model data/ml_training.csv data/runtime_model.csv results/ml_validation.csv
// (or simply ./scripts/ml_train.sh, which also adds new measurements first)
//
// Input: the training set, one row per measured run, each task run ALONE on
// its machine. Columns found by name, other columns ignored:
//
//	workload,size,runtime_s,max_rss_kb,cpu_model
//
// cpu_model is the CPU platform read by lscpu ("Model name [family/model]").
// Runs shorter than -min-runtime (0.5 s) are not used: /usr/bin/time has a
// resolution of 10 ms, so a 0.04 s run has an error of 25 %.
//
// The model, for each workload w (three equations):
//
//  1. Size law, learned on one reference platform R:
//     runtime_R(size) = a * size^b
//     a and b are found by least squares on log(runtime) = log(a) + b*log(size),
//     a straight line in log-log scale. b is compared with the theory of TD1.
//     R is the platform with the most distinct sizes for this workload.
//  2. Speed factor of each platform p (R included):
//     f_p = median over the runs of p of (measured runtime / runtime_R(size))
//     so that runtime_p(size) = a * size^b * f_p
//     f_p = 4 means "4 times slower than R on this workload". A platform never
//     measured gets f = 1 in the scheduler (with a warning).
//  3. Memory, the same way, but with all platforms together (RSS does not
//     depend on the CPU): max_rss_kb(size) = c * size^d
//
// Validation, leave-one-size-out: for each size s of a workload, all the runs
// of size s are removed, the model is trained again on the other sizes, and
// it predicts size s on every platform where s was measured. The error is
// |predicted - measured mean| / measured mean. Its mean is the MAPE, given
// per workload and platform, next to the MAPE of the same model without the
// speed factor (every platform as fast as R), which is what a model that
// ignores the CPU would do.
//
// Outputs:
//
//	MODEL       workload,platform,role,a,b,speed_factor,rss_c,rss_d,min_size,max_size,runs
//	            read by scheduler/lpt_scheduler.go (-model)
//	VALIDATION  MAPE per workload and platform (+ VALIDATION_detail.csv: one
//	            row per held-out size and platform)
//
// Go standard library only.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Point is one measured run.
type Point struct {
	Size, Runtime, RSSKB float64
}

// Law is y = A * x^B.
type Law struct {
	A, B, R2 float64
}

func (l Law) Predict(x float64) float64 { return l.A * math.Pow(x, l.B) }

// fitPowerLaw finds A and B by least squares on log(y) = log(A) + B*log(x).
// It needs at least two different values of x.
func fitPowerLaw(xs, ys []float64) (Law, error) {
	n := float64(len(xs))
	if len(xs) != len(ys) || len(xs) == 0 {
		return Law{}, fmt.Errorf("no point")
	}
	var sx, sy float64
	for i := range xs {
		if xs[i] <= 0 || ys[i] <= 0 {
			return Law{}, fmt.Errorf("size and value must be > 0 (got %g, %g)", xs[i], ys[i])
		}
		sx += math.Log(xs[i])
		sy += math.Log(ys[i])
	}
	mx, my := sx/n, sy/n
	var sxx, sxy, syy float64
	for i := range xs {
		dx, dy := math.Log(xs[i])-mx, math.Log(ys[i])-my
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	if sxx < 1e-12 {
		return Law{}, fmt.Errorf("at least two different sizes are needed")
	}
	b := sxy / sxx
	r2 := 1.0
	if syy > 0 {
		r2 = sxy * sxy / (sxx * syy)
	}
	return Law{A: math.Exp(my - b*mx), B: b, R2: r2}, nil
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// distinctSizes returns the sorted distinct sizes of a list of points.
func distinctSizes(points []Point) []float64 {
	seen := make(map[float64]bool)
	var sizes []float64
	for _, p := range points {
		if !seen[p.Size] {
			seen[p.Size] = true
			sizes = append(sizes, p.Size)
		}
	}
	sort.Float64s(sizes)
	return sizes
}

func sortedKeys(m map[string][]Point) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PlatformModel is the speed factor of one platform for one workload.
type PlatformModel struct {
	Platform, Role   string // Role: "reference" or "measured"
	Factor           float64
	Runs, Sizes      int
	MinSize, MaxSize float64
}

// WorkloadModel is the learned model of one workload.
type WorkloadModel struct {
	Workload, Reference string
	Runtime             Law // on the reference platform
	RSS                 Law // all platforms together
	Platforms           []PlatformModel
}

// Factor returns the speed factor of a platform (false if never measured).
func (m WorkloadModel) Factor(platform string) (float64, bool) {
	for _, p := range m.Platforms {
		if p.Platform == platform {
			return p.Factor, true
		}
	}
	return 0, false
}

// chooseReference: the platform with the most distinct sizes, then the most
// runs, then the first name in alphabetical order (deterministic).
func chooseReference(byPlatform map[string][]Point) (string, error) {
	best, bestSizes, bestRuns := "", 0, 0
	for _, p := range sortedKeys(byPlatform) {
		s, r := len(distinctSizes(byPlatform[p])), len(byPlatform[p])
		if s > bestSizes || (s == bestSizes && r > bestRuns) {
			best, bestSizes, bestRuns = p, s, r
		}
	}
	if bestSizes < 2 {
		return "", fmt.Errorf("no platform has runs of at least two different sizes")
	}
	return best, nil
}

// trainWorkload applies the three equations to the runs of one workload.
func trainWorkload(name string, byPlatform map[string][]Point) (WorkloadModel, error) {
	ref, err := chooseReference(byPlatform)
	if err != nil {
		return WorkloadModel{}, fmt.Errorf("%s: %v", name, err)
	}
	m := WorkloadModel{Workload: name, Reference: ref}

	// 1. size law on the reference platform
	var xs, ys []float64
	for _, p := range byPlatform[ref] {
		xs = append(xs, p.Size)
		ys = append(ys, p.Runtime)
	}
	if m.Runtime, err = fitPowerLaw(xs, ys); err != nil {
		return WorkloadModel{}, fmt.Errorf("%s: %v", name, err)
	}

	// 2. one speed factor per platform: median of measured / predicted on R
	for _, platform := range sortedKeys(byPlatform) {
		points := byPlatform[platform]
		ratios := make([]float64, len(points))
		for i, p := range points {
			ratios[i] = p.Runtime / m.Runtime.Predict(p.Size)
		}
		sizes := distinctSizes(points)
		role := "measured"
		if platform == ref {
			role = "reference"
		}
		m.Platforms = append(m.Platforms, PlatformModel{
			Platform: platform, Role: role, Factor: median(ratios),
			Runs: len(points), Sizes: len(sizes), MinSize: sizes[0], MaxSize: sizes[len(sizes)-1],
		})
	}
	sort.SliceStable(m.Platforms, func(i, j int) bool { return m.Platforms[i].Role == "reference" && m.Platforms[j].Role != "reference" })

	// 3. memory law, all platforms together
	xs, ys = nil, nil
	for _, platform := range sortedKeys(byPlatform) {
		for _, p := range byPlatform[platform] {
			xs = append(xs, p.Size)
			ys = append(ys, p.RSSKB)
		}
	}
	if m.RSS, err = fitPowerLaw(xs, ys); err != nil {
		return WorkloadModel{}, fmt.Errorf("%s (memory): %v", name, err)
	}
	return m, nil
}

// theoryExponent is the slope of log(runtime) against log(size) given by the
// complexity measured in TD1, around the geometric mean size N of the data.
// For N^1.5/ln N the local slope is 1.5 - 1/ln N, for N log N it is 1 + 1/ln N.
func theoryExponent(workload string, sizes []float64) (float64, string, bool) {
	logSum := 0.0
	for _, s := range sizes {
		logSum += math.Log(s)
	}
	lnN := logSum / float64(len(sizes))
	switch workload {
	case "cpu_task":
		return 1.5 - 1/lnN, "N^1.5/ln N", true
	case "memory_task":
		return 1, "N", true
	case "matrix_task":
		return 3, "N^3", true
	case "sort_task":
		return 1 + 1/lnN, "N log N", true
	}
	return 0, "", false
}

// Check is one leave-one-size-out prediction.
type Check struct {
	Workload, Platform, Position string
	Size                         float64
	Measured, Predicted          float64 // mean runtime of the runs of this size, prediction
	NoFactor                     float64 // prediction with factor 1 (CPU ignored)
	MeasuredRSS, PredictedRSS    float64
}

func pctError(predicted, measured float64) float64 {
	return 100 * math.Abs(predicted-measured) / measured
}

// validateWorkload is the leave-one-size-out validation of one workload.
// It returns the checks and the sizes that could not be checked.
func validateWorkload(name string, byPlatform map[string][]Point) []Check {
	var all []Point
	for _, platform := range sortedKeys(byPlatform) {
		all = append(all, byPlatform[platform]...)
	}
	var checks []Check
	for _, s := range distinctSizes(all) {
		// training set without size s
		reduced := make(map[string][]Point)
		for platform, points := range byPlatform {
			for _, p := range points {
				if p.Size != s {
					reduced[platform] = append(reduced[platform], p)
				}
			}
		}
		m, err := trainWorkload(name, reduced)
		if err != nil {
			continue // not enough sizes left: this size cannot be checked
		}
		lawSizes := distinctSizes(reduced[m.Reference])
		position := "inside"
		if s < lawSizes[0] || s > lawSizes[len(lawSizes)-1] {
			position = "outside" // extrapolation beyond the sizes of the law
		}
		for _, platform := range sortedKeys(byPlatform) {
			var rt, rss []float64
			for _, p := range byPlatform[platform] {
				if p.Size == s {
					rt = append(rt, p.Runtime)
					rss = append(rss, p.RSSKB)
				}
			}
			f, known := m.Factor(platform)
			if len(rt) == 0 || !known {
				continue // not measured at this size, or only at this size
			}
			checks = append(checks, Check{
				Workload: name, Platform: platform, Position: position, Size: s,
				Measured: mean(rt), Predicted: m.Runtime.Predict(s) * f, NoFactor: m.Runtime.Predict(s),
				MeasuredRSS: mean(rss), PredictedRSS: m.RSS.Predict(s),
			})
		}
	}
	return checks
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// spreadPct: mean over the sizes run at least twice of (max - min) / mean of
// the runtimes, the run-to-run variability already used in TD1 and TD2. A
// model cannot be much more precise than this noise. Empty if no size was
// run twice.
func spreadPct(points []Point) string {
	var spreads []float64
	for _, s := range distinctSizes(points) {
		var rt []float64
		for _, p := range points {
			if p.Size == s {
				rt = append(rt, p.Runtime)
			}
		}
		if len(rt) < 2 {
			continue
		}
		sort.Float64s(rt)
		spreads = append(spreads, 100*(rt[len(rt)-1]-rt[0])/mean(rt))
	}
	if len(spreads) == 0 {
		return ""
	}
	return f1(mean(spreads))
}

// readTraining reads the training set: workload -> platform -> runs.
func readTraining(path string, minRuntime float64) (map[string]map[string][]Point, int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	if len(records) < 2 {
		return nil, 0, 0, fmt.Errorf("%s has no data row", path)
	}
	position := make(map[string]int)
	for i, name := range records[0] {
		position[strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF"))] = i
	}
	required := []string{"workload", "size", "runtime_s", "max_rss_kb", "cpu_model"}
	for _, name := range required {
		if _, ok := position[name]; !ok {
			return nil, 0, 0, fmt.Errorf("%s: column %q not found (header must contain %s)", path, name, strings.Join(required, ","))
		}
	}
	data := make(map[string]map[string][]Point)
	used, short := 0, 0
	for i, r := range records[1:] {
		get := func(name string) string { return strings.TrimSpace(r[position[name]]) }
		size, err1 := strconv.ParseFloat(get("size"), 64)
		runtime, err2 := strconv.ParseFloat(get("runtime_s"), 64)
		rss, err3 := strconv.ParseFloat(get("max_rss_kb"), 64)
		workload, platform := get("workload"), get("cpu_model")
		if err1 != nil || err2 != nil || err3 != nil || size <= 0 || runtime < 0 || rss <= 0 || workload == "" || platform == "" {
			return nil, 0, 0, fmt.Errorf("%s, data row %d: need a workload, a cpu_model, size > 0, runtime_s >= 0 and max_rss_kb > 0", path, i+1)
		}
		if runtime < minRuntime {
			short++
			continue
		}
		if data[workload] == nil {
			data[workload] = make(map[string][]Point)
		}
		data[workload][platform] = append(data[workload][platform], Point{Size: size, Runtime: runtime, RSSKB: rss})
		used++
	}
	return data, used, short, nil
}

// writeCSV writes rows through a temporary file, so that a half-written file never exists.
func writeCSV(path string, rows [][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-model-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := csv.NewWriter(tmp)
	w.WriteAll(rows)
	err = w.Error()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

func g(x float64) string  { return strconv.FormatFloat(x, 'g', 6, 64) }
func f1(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }
func f3(x float64) string { return strconv.FormatFloat(x, 'f', 3, 64) }

// num writes a size as an integer (8000000, not 8e+06)
func num(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// modelRows is the content of the model file read by the scheduler.
func modelRows(models []WorkloadModel) [][]string {
	rows := [][]string{{"workload", "platform", "role", "a", "b", "speed_factor", "rss_c", "rss_d", "min_size", "max_size", "runs"}}
	for _, m := range models {
		for _, p := range m.Platforms {
			rows = append(rows, []string{m.Workload, p.Platform, p.Role, g(m.Runtime.A), g(m.Runtime.B), g(p.Factor),
				g(m.RSS.A), g(m.RSS.B), num(p.MinSize), num(p.MaxSize), strconv.Itoa(p.Runs)})
		}
	}
	return rows
}

// summary aggregates the checks of one group (a platform, or a whole workload).
type summary struct {
	n                         int
	mape, noFactor, max, rssM float64
}

func summarize(checks []Check, keep func(Check) bool) summary {
	var s summary
	for _, c := range checks {
		if !keep(c) {
			continue
		}
		e := pctError(c.Predicted, c.Measured)
		s.n++
		s.mape += e
		s.noFactor += pctError(c.NoFactor, c.Measured)
		s.rssM += pctError(c.PredictedRSS, c.MeasuredRSS)
		s.max = math.Max(s.max, e)
	}
	if s.n > 0 {
		s.mape /= float64(s.n)
		s.noFactor /= float64(s.n)
		s.rssM /= float64(s.n)
	}
	return s
}

// pct shows a cell of cells() on the screen: "3.2%", or "-" when there is no check
func pct(cell string) string {
	if cell == "" {
		return "-"
	}
	return cell + "%"
}

func (s summary) cells() []string {
	if s.n == 0 {
		return []string{"0", "", "", "", ""}
	}
	return []string{strconv.Itoa(s.n), f1(s.mape), f1(s.noFactor), f1(s.max), f1(s.rssM)}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("runtime_model", flag.ContinueOnError)
	minRuntime := flags.Float64("min-runtime", 0.5, "runs shorter than this (seconds) are not used")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 3 {
		return fmt.Errorf("usage: runtime_model [-min-runtime 0.5] <training.csv> <model.csv> <validation.csv>")
	}
	trainingPath, modelPath, validationPath := flags.Arg(0), flags.Arg(1), flags.Arg(2)

	data, used, short, err := readTraining(trainingPath, *minRuntime)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Training set %s: %d runs used, %d shorter than %.2f s ignored\n", trainingPath, used, short, *minRuntime)

	summaryRows := [][]string{{"workload", "platform", "role", "speed_factor", "b", "b_theory", "sizes", "runs",
		"spread_pct", "checks", "mape_pct", "mape_no_factor_pct", "max_error_pct", "rss_mape_pct"}}
	detailRows := [][]string{{"workload", "platform", "size", "position", "measured_s", "predicted_s", "error_pct",
		"predicted_no_factor_s", "error_no_factor_pct", "measured_rss_kb", "predicted_rss_kb", "rss_error_pct"}}
	var models []WorkloadModel
	var allChecks []Check

	workloads := make([]string, 0, len(data))
	for w := range data {
		workloads = append(workloads, w)
	}
	sort.Strings(workloads)
	for _, w := range workloads {
		m, err := trainWorkload(w, data[w])
		if err != nil {
			fmt.Fprintf(out, "\nWARNING %v: %s is NOT in the model (measure it on one platform at 2 sizes or more)\n", err, w)
			continue
		}
		models = append(models, m)
		checks := validateWorkload(w, data[w])
		allChecks = append(allChecks, checks...)

		refSizes := distinctSizes(data[w][m.Reference])
		bTheory, formula, ok := theoryExponent(w, refSizes)
		theory := "no theory for this workload"
		bTheoryCell := ""
		if ok {
			theory = fmt.Sprintf("theory %s gives b = %.2f around these sizes", formula, bTheory)
			bTheoryCell = f3(bTheory)
		}
		fmt.Fprintf(out, "\n%s  runtime = %.4g x size^%.3f on %s (reference, R2 %.4f)\n", w, m.Runtime.A, m.Runtime.B, m.Reference, m.Runtime.R2)
		fmt.Fprintf(out, "  %s; max RSS = %.4g x size^%.3f kB (all platforms)\n", theory, m.RSS.A, m.RSS.B)
		width := len("all platforms")
		for _, p := range m.Platforms {
			width = max(width, len(p.Platform))
		}
		fmt.Fprintf(out, "  %-*s %7s %5s %4s %7s | %6s %9s %10s %8s %8s\n", width, "platform", "factor", "sizes", "runs", "spread",
			"checks", "LOSO MAPE", "no factor", "max err", "RSS MAPE")
		for _, p := range m.Platforms {
			s := summarize(checks, func(c Check) bool { return c.Platform == p.Platform })
			bCell, bTheoryRow := "", ""
			if p.Role == "reference" {
				bCell, bTheoryRow = f3(m.Runtime.B), bTheoryCell
			}
			cells := s.cells()
			summaryRows = append(summaryRows, append([]string{w, p.Platform, p.Role, f3(p.Factor), bCell, bTheoryRow,
				strconv.Itoa(p.Sizes), strconv.Itoa(p.Runs), spreadPct(data[w][p.Platform])}, cells...))
			fmt.Fprintf(out, "  %-*s %7.3f %5d %4d %7s | %6s %9s %10s %8s %8s\n", width, p.Platform, p.Factor, p.Sizes, p.Runs,
				pct(spreadPct(data[w][p.Platform])), cells[0], pct(cells[1]), pct(cells[2]), pct(cells[3]), pct(cells[4]))
		}
		s := summarize(checks, func(c Check) bool { return true })
		summaryRows = append(summaryRows, append([]string{w, "ALL", "", "", "", "", "", "", ""}, s.cells()...))
		cells := s.cells()
		fmt.Fprintf(out, "  %-*s %7s %5s %4s %7s | %6s %9s %10s %8s %8s\n", width, "all platforms", "", "", "", "",
			cells[0], pct(cells[1]), pct(cells[2]), pct(cells[3]), pct(cells[4]))
		for _, c := range checks {
			detailRows = append(detailRows, []string{c.Workload, c.Platform, num(c.Size), c.Position, f3(c.Measured), f3(c.Predicted),
				f1(pctError(c.Predicted, c.Measured)), f3(c.NoFactor), f1(pctError(c.NoFactor, c.Measured)),
				strconv.FormatFloat(c.MeasuredRSS, 'f', 0, 64), strconv.FormatFloat(c.PredictedRSS, 'f', 0, 64),
				f1(pctError(c.PredictedRSS, c.MeasuredRSS))})
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("no workload could be trained: nothing written")
	}
	s := summarize(allChecks, func(c Check) bool { return true })
	inside := summarize(allChecks, func(c Check) bool { return c.Position == "inside" })
	summaryRows = append(summaryRows, append([]string{"ALL", "ALL", "", "", "", "", "", "", ""}, s.cells()...))
	fmt.Fprintf(out, "\nLeave-one-size-out, all workloads: MAPE %.1f %% over %d checks (%.1f %% without the speed factor);\n",
		s.mape, s.n, s.noFactor)
	if inside.n > 0 {
		fmt.Fprintf(out, "sizes inside the measured range (interpolation): MAPE %.1f %% over %d checks\n", inside.mape, inside.n)
	}

	detailPath := strings.TrimSuffix(validationPath, ".csv") + "_detail.csv"
	if err := writeCSV(modelPath, modelRows(models)); err != nil {
		return fmt.Errorf("write %s: %w", modelPath, err)
	}
	if err := writeCSV(validationPath, summaryRows); err != nil {
		return fmt.Errorf("write %s: %w", validationPath, err)
	}
	if err := writeCSV(detailPath, detailRows); err != nil {
		return fmt.Errorf("write %s: %w", detailPath, err)
	}
	fmt.Fprintf(out, "Model written to %s, validation to %s and %s\n", modelPath, validationPath, detailPath)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

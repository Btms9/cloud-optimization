// runtime_model_test.go - tests of the runtime model (no VM, no GCP needed).
//
// Run: go test -v ml/runtime_model.go ml/runtime_model_test.go
package main

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func near(got, want, relTol float64) bool {
	return math.Abs(got-want) <= relTol*math.Abs(want)
}

func TestFitRecoversAKnownPowerLaw(t *testing.T) {
	// y = 2e-3 * x^1.5 exactly
	xs := []float64{100, 200, 400, 800}
	var ys []float64
	for _, x := range xs {
		ys = append(ys, 2e-3*math.Pow(x, 1.5))
	}
	law, err := fitPowerLaw(xs, ys)
	if err != nil {
		t.Fatal(err)
	}
	if !near(law.A, 2e-3, 1e-9) || !near(law.B, 1.5, 1e-9) {
		t.Fatalf("got a=%g b=%g, want 2e-3 and 1.5", law.A, law.B)
	}
}

func TestFitNeedsTwoSizes(t *testing.T) {
	if _, err := fitPowerLaw([]float64{100, 100}, []float64{1, 1.1}); err == nil {
		t.Fatal("one size only: an error was expected")
	}
	if _, err := fitPowerLaw([]float64{100, 200}, []float64{1, 0}); err == nil {
		t.Fatal("a zero runtime cannot be put in log scale: an error was expected")
	}
}

func TestMedian(t *testing.T) {
	if m := median([]float64{3, 1, 100}); m != 3 {
		t.Errorf("median of 3 1 100 = %g, want 3 (the outlier 100 is ignored)", m)
	}
	if m := median([]float64{4, 1, 3, 2}); m != 2.5 {
		t.Errorf("median of 4 values = %g, want 2.5", m)
	}
}

// Synthetic platforms: "fast" follows 1e-6 * size^1.5, "slow" is 4 times slower.
func synthetic(sizesFast, sizesSlow []float64) map[string][]Point {
	data := map[string][]Point{}
	for _, s := range sizesFast {
		for run := 0; run < 3; run++ {
			data["fast"] = append(data["fast"], Point{Size: s, Runtime: 1e-6 * math.Pow(s, 1.5), RSSKB: 8 * s})
		}
	}
	for _, s := range sizesSlow {
		data["slow"] = append(data["slow"], Point{Size: s, Runtime: 4e-6 * math.Pow(s, 1.5), RSSKB: 8 * s})
	}
	return data
}

func TestSpeedFactorAndReference(t *testing.T) {
	// "fast" has 4 sizes, "slow" 2: "fast" must be the reference
	m, err := trainWorkload("w", synthetic([]float64{1e4, 2e4, 4e4, 8e4}, []float64{1e4, 3e4}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Reference != "fast" || m.Platforms[0].Platform != "fast" || m.Platforms[0].Role != "reference" {
		t.Fatalf("reference = %s (first row %+v), want fast", m.Reference, m.Platforms[0])
	}
	if f, ok := m.Factor("slow"); !ok || !near(f, 4, 1e-9) {
		t.Errorf("speed factor of slow = %g, want 4", f)
	}
	if f, _ := m.Factor("fast"); !near(f, 1, 1e-9) {
		t.Errorf("speed factor of the reference = %g, want 1", f)
	}
	if !near(m.RSS.A, 8, 1e-9) || !near(m.RSS.B, 1, 1e-9) {
		t.Errorf("RSS law %g x size^%g, want 8 x size^1", m.RSS.A, m.RSS.B)
	}
	if _, ok := m.Factor("never-measured"); ok {
		t.Error("a platform never measured must not have a factor")
	}
}

func TestReferenceTieIsDeterministic(t *testing.T) {
	data := map[string][]Point{
		"b-platform": {{1, 1, 1}, {2, 2, 1}},
		"a-platform": {{1, 1, 1}, {2, 2, 1}},
	}
	ref, err := chooseReference(data)
	if err != nil || ref != "a-platform" {
		t.Fatalf("reference %q (%v), want a-platform (same sizes and runs: alphabetical)", ref, err)
	}
}

func TestLeaveOneSizeOutOnExactDataHasNoError(t *testing.T) {
	data := synthetic([]float64{1e4, 2e4, 4e4, 8e4}, []float64{1e4, 2e4, 4e4})
	checks := validateWorkload("w", data)
	// fast: 4 sizes; slow: 3 sizes, each can be removed (other sizes remain)
	if len(checks) != 7 {
		t.Fatalf("%d checks, want 7", len(checks))
	}
	for _, c := range checks {
		if pctError(c.Predicted, c.Measured) > 1e-6 {
			t.Errorf("%s size %g: predicted %g, measured %g", c.Platform, c.Size, c.Predicted, c.Measured)
		}
		if c.Platform == "slow" && !near(pctError(c.NoFactor, c.Measured), 75, 1e-6) {
			t.Errorf("without the factor, slow should be predicted 4 times too fast (75 %% error), got %.1f %%",
				pctError(c.NoFactor, c.Measured))
		}
	}
}

func TestPlatformMeasuredAtOneSizeIsNotValidatedAtThatSize(t *testing.T) {
	// slow has only size 2e4: when 2e4 is removed, slow has no factor left
	checks := validateWorkload("w", synthetic([]float64{1e4, 2e4, 4e4}, []float64{2e4}))
	for _, c := range checks {
		if c.Platform == "slow" {
			t.Fatalf("slow was validated at %g although it has no other size", c.Size)
		}
	}
}

func TestTheoryExponents(t *testing.T) {
	b, _, _ := theoryExponent("cpu_task", []float64{8e6, 16e6, 32e6})
	if !near(b, 1.5-1/math.Log(16e6), 1e-9) {
		t.Errorf("cpu_task theory %g", b)
	}
	if b, _, _ := theoryExponent("matrix_task", []float64{1000}); b != 3 {
		t.Errorf("matrix_task theory %g, want 3", b)
	}
	if _, _, ok := theoryExponent("gpu_task", []float64{1}); ok {
		t.Error("unknown workload: no theory expected")
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShortRunsAndExtraColumns(t *testing.T) {
	dir := t.TempDir()
	// columns in another order, an extra column, one run too short (0.2 s)
	training := writeFile(t, dir, "train.csv", "source,cpu_model,workload,size,runtime_s,max_rss_kb,run\n"+
		"x,P1,w,100,1.0,1000,1\nx,P1,w,200,2.0,2000,1\nx,P1,w,50,0.2,500,1\n")
	data, used, short, err := readTraining(training, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if used != 2 || short != 1 || len(data["w"]["P1"]) != 2 {
		t.Fatalf("used %d short %d, want 2 and 1", used, short)
	}
	for _, bad := range []string{
		"workload,size,runtime_s,max_rss_kb\nw,100,1,1000\n",             // no cpu_model
		"workload,size,runtime_s,max_rss_kb,cpu_model\nw,abc,1,1000,P\n", // bad size
		"workload,size,runtime_s,max_rss_kb,cpu_model\nw,100,1,1000,\n",  // empty platform
	} {
		if _, _, _, err := readTraining(writeFile(t, dir, "bad.csv", bad), 0.5); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
}

func TestRunWritesModelAndValidation(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("workload,size,run,runtime_s,max_rss_kb,machine_type,cpu_model,vm,source\n")
	for _, s := range []float64{1000, 2000, 4000} {
		for run := 1; run <= 3; run++ {
			b.WriteString("w,")
			b.WriteString(num(s))
			b.WriteString(",1,")
			b.WriteString(g(1e-3 * s))
			b.WriteString(",5000,t,FAST [1/1],vm,src\n")
			b.WriteString("w,")
			b.WriteString(num(s))
			b.WriteString(",1,")
			b.WriteString(g(3e-3 * s))
			b.WriteString(",5000,t,SLOW [2/2],vm,src\n")
		}
	}
	training := writeFile(t, dir, "train.csv", b.String())
	model, validation := filepath.Join(dir, "model.csv"), filepath.Join(dir, "val.csv")
	if err := run([]string{training, model, validation}, io.Discard); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(model)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if lines[0] != "workload,platform,role,a,b,speed_factor,rss_c,rss_d,runs" || len(lines) != 3 {
		t.Fatalf("model file:\n%s", content)
	}
	if !strings.HasPrefix(lines[1], "w,FAST [1/1],reference,") || !strings.Contains(lines[2], "SLOW [2/2],measured,") ||
		!strings.Contains(lines[2], ",3,") || !strings.HasSuffix(lines[2], ",9") {
		t.Errorf("model rows:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "val_detail.csv")); err != nil {
		t.Error("val_detail.csv not written")
	}
	content, _ = os.ReadFile(validation)
	if !strings.Contains(string(content), "ALL,ALL,") {
		t.Errorf("validation file has no global row:\n%s", content)
	}
}

func TestRunWithoutUsableDataWritesNothing(t *testing.T) {
	dir := t.TempDir()
	training := writeFile(t, dir, "train.csv", "workload,size,runtime_s,max_rss_kb,cpu_model\nw,100,1,1000,P\n")
	model := filepath.Join(dir, "model.csv")
	if err := run([]string{training, model, filepath.Join(dir, "v.csv")}, io.Discard); err == nil {
		t.Fatal("one size only: an error was expected")
	}
	if _, err := os.Stat(model); err == nil {
		t.Fatal("a model was written without any trained workload")
	}
}

func TestOurTrainingSet(t *testing.T) {
	// The real data of the repository: the exponents must match the theory of TD1
	path := filepath.Join("..", "data", "ml_training.csv")
	if _, err := os.Stat(path); err != nil {
		t.Skip("data/ml_training.csv not found")
	}
	data, _, _, err := readTraining(path, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	for w, tol := range map[string]float64{"cpu_task": 0.05, "memory_task": 0.05, "matrix_task": 0.1} {
		m, err := trainWorkload(w, data[w])
		if err != nil {
			t.Fatal(err)
		}
		bt, _, _ := theoryExponent(w, distinctSizes(data[w][m.Reference]))
		if math.Abs(m.Runtime.B-bt) > tol {
			t.Errorf("%s: b = %.3f, theory %.3f", w, m.Runtime.B, bt)
		}
	}
}

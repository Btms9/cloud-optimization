# test_fit_model.py - tests of scripts/fit_model.py (the regression of step 1).
#
# Run, from the project folder:  python -m unittest tests/test_fit_model.py
#
# No VM and no GCP access are needed. Most tests build artificial measurements
# that follow an exact power law, so the expected coefficients are known in
# advance; the last ones run the script on the real files of data/.
import contextlib
import csv
import importlib.util
import io
import math
import os
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "fit_model", os.path.join(ROOT, "scripts", "fit_model.py"))
fm = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fm)

CPU = "Intel(R) Xeon(R) CPU @ 2.20GHz"


def synth(workload, sizes, a=2e-9, b=1.5, c=8.0, k=1.0, slower=1.0, runs=3):
    """Artificial measurements: runtime = slower * a * size^b, RSS = c * size^k."""
    return [fm.Run(workload, s, slower * a * s ** b, c * s ** k)
            for s in sizes for _ in range(runs)]


def row_of(rows, workload, machine_type):
    for r in rows:
        if r["workload"] == workload and r["machine_type"] == machine_type:
            return r
    raise AssertionError("no row for %s on %s" % (workload, machine_type))


def cpus(*types):
    return {t: (CPU, "lscpu") for t in types}


class FitPowerLaw(unittest.TestCase):
    def test_recovers_the_coefficients_of_an_exact_power_law(self):
        sizes = [1000, 2000, 4000, 8000]
        coef, exponent = fm.fit_power_law(sizes, [3e-6 * s ** 1.5 for s in sizes])
        self.assertAlmostEqual(exponent, 1.5, places=9)
        self.assertAlmostEqual(coef / 3e-6, 1.0, places=9)

    def test_a_constant_value_gives_exponent_zero(self):
        coef, exponent = fm.fit_power_law([10, 100, 1000], [2500, 2500, 2500])
        self.assertAlmostEqual(exponent, 0.0, places=9)
        self.assertAlmostEqual(coef, 2500, places=6)


class BuildModel(unittest.TestCase):
    def setUp(self):
        # "big" has the full campaign, "small" two control sizes 10 % slower,
        # "none" has no measurement of this workload at all.
        self.data = {
            "big": synth("cpu_task", [1000, 2000, 4000, 8000]),
            "small": synth("cpu_task", [2000, 4000], slower=1.10),
            "none": synth("other_task", [10, 20, 30]),
        }
        self.rows = fm.build_model(self.data, cpus("big", "small", "none"))

    def test_one_row_per_workload_and_machine_type(self):
        keys = sorted((r["workload"], r["machine_type"]) for r in self.rows)
        self.assertEqual(keys, [
            ("cpu_task", "big"), ("cpu_task", "none"), ("cpu_task", "small"),
            ("other_task", "big"), ("other_task", "none"), ("other_task", "small")])

    def test_reference_is_the_type_with_the_most_sizes(self):
        r = row_of(self.rows, "cpu_task", "big")
        self.assertEqual(r["factor_source"], "reference")
        self.assertEqual(r["factor"], 1.0)
        self.assertAlmostEqual(r["b"], 1.5, places=9)
        self.assertAlmostEqual(r["a"] / 2e-9, 1.0, places=9)

    def test_control_points_give_a_measured_factor(self):
        r = row_of(self.rows, "cpu_task", "small")
        self.assertEqual(r["factor_source"], "measured")
        self.assertAlmostEqual(r["factor"], 1.10, places=9)
        # a and b are those of the reference: only the factor changes
        self.assertAlmostEqual(r["b"], 1.5, places=9)
        self.assertAlmostEqual(r["a"] / 2e-9, 1.0, places=9)

    def test_no_control_point_gives_factor_one_marked_assumed(self):
        r = row_of(self.rows, "cpu_task", "none")
        self.assertEqual(r["factor_source"], "assumed")
        self.assertEqual(r["factor"], 1.0)

    def test_memory_is_fitted_on_all_machine_types_together(self):
        for machine_type in ("big", "small", "none"):
            r = row_of(self.rows, "cpu_task", machine_type)
            self.assertAlmostEqual(r["k"], 1.0, places=9)
            self.assertAlmostEqual(r["c"], 8.0, places=6)

    def test_memory_fit_uses_the_runs_of_the_control_types_too(self):
        # "small" reports 1.5 times more memory on its two sizes, which sit in
        # the middle of the range: the slope stays 1, and since 6 runs out of
        # 18 are 1.5 times higher, c is multiplied by 1.5^(6/18).
        data = {"big": synth("cpu_task", [1000, 2000, 4000, 8000]),
                "small": synth("cpu_task", [2000, 4000], c=12.0)}
        r = row_of(fm.build_model(data, cpus("big", "small")), "cpu_task", "big")
        self.assertAlmostEqual(r["k"], 1.0, places=9)
        self.assertAlmostEqual(r["c"], 8.0 * 1.5 ** (1 / 3), places=6)

    def test_size_range_is_the_one_of_the_reference_campaign(self):
        for machine_type in ("big", "small", "none"):
            r = row_of(self.rows, "cpu_task", machine_type)
            self.assertEqual((r["size_min"], r["size_max"]), (1000, 8000))

    def test_cpu_model_and_source_are_copied_per_machine_type(self):
        rows = fm.build_model(self.data, {
            "big": (CPU, "declared"), "small": (CPU, "lscpu"), "none": (CPU, "lscpu")})
        self.assertEqual(row_of(rows, "cpu_task", "big")["cpu_model"], CPU)
        self.assertEqual(row_of(rows, "cpu_task", "big")["cpu_source"], "declared")
        self.assertEqual(row_of(rows, "cpu_task", "small")["cpu_source"], "lscpu")

    def test_tie_on_the_number_of_sizes_keeps_the_first_type(self):
        data = {"first": synth("cpu_task", [1, 2, 3]),
                "second": synth("cpu_task", [1, 2, 3], slower=2.0)}
        rows = fm.build_model(data, cpus("first", "second"))
        self.assertEqual(row_of(rows, "cpu_task", "first")["factor_source"], "reference")
        self.assertAlmostEqual(row_of(rows, "cpu_task", "second")["factor"], 2.0, places=9)


class BuildModelErrors(unittest.TestCase):
    def test_fewer_than_three_sizes_is_refused(self):
        data = {"big": synth("cpu_task", [1000, 2000])}
        with self.assertRaisesRegex(ValueError, "at least 3 sizes"):
            fm.build_model(data, cpus("big"))

    def test_different_cpu_models_are_refused(self):
        data = {"big": synth("cpu_task", [1, 2, 3]), "small": synth("cpu_task", [2])}
        with self.assertRaisesRegex(ValueError, "same CPU"):
            fm.build_model(data, {"big": (CPU, "lscpu"), "small": ("AMD EPYC 7B12", "lscpu")})

    def test_no_measurement_at_all_is_refused(self):
        with self.assertRaisesRegex(ValueError, "no measurement"):
            fm.build_model({"big": []}, cpus("big"))


class LeaveOneSizeOut(unittest.TestCase):
    def test_exact_power_law_has_no_error(self):
        runs = synth("cpu_task", [1000, 2000, 4000, 8000])
        checks = fm.leave_one_size_out([r.size for r in runs], [r.runtime_s for r in runs])
        self.assertEqual([c.size for c in checks], [1000, 2000, 4000, 8000])
        for c in checks:
            self.assertAlmostEqual(c.error, 0.0, places=9)

    def test_end_sizes_are_extrapolations_and_inner_sizes_are_not(self):
        runs = synth("cpu_task", [1000, 2000, 4000, 8000])
        checks = fm.leave_one_size_out([r.size for r in runs], [r.runtime_s for r in runs])
        self.assertEqual([c.extrapolated for c in checks], [True, False, False, True])

    def test_the_size_left_out_is_not_used_for_the_fit(self):
        # Size 2000 is measured twice too slow. The fit on the other sizes is
        # still the exact law, so the prediction is half of the measurement.
        sizes = [1000, 2000, 4000, 8000]
        values = [s ** 2.0 for s in sizes]
        values[1] *= 2.0
        checks = fm.leave_one_size_out(sizes, values)
        self.assertAlmostEqual(checks[1].predicted, 2000 ** 2.0, delta=1e-3)
        self.assertAlmostEqual(checks[1].error, -0.5, places=9)

    def test_runs_of_one_size_are_summarized_by_the_mean_or_the_max(self):
        sizes = [1, 1, 2, 2, 4, 4]
        values = [10, 30, 40, 40, 80, 80]
        self.assertAlmostEqual(fm.leave_one_size_out(sizes, values)[0].measured, 20)
        self.assertAlmostEqual(fm.leave_one_size_out(sizes, values, summary=max)[0].measured, 30)


class CheckFactor(unittest.TestCase):
    def test_factor_from_one_size_predicts_the_other(self):
        control = synth("cpu_task", [2000, 4000], slower=1.10)
        errors = fm.check_factor(control, 2e-9, 1.5)
        self.assertEqual(len(errors), 2)
        for e in errors:
            self.assertAlmostEqual(e, 0.0, places=9)

    def test_factors_that_differ_between_sizes_show_as_an_error(self):
        control = (synth("cpu_task", [2000], slower=1.10)
                   + synth("cpu_task", [4000], slower=1.21))
        errors = fm.check_factor(control, 2e-9, 1.5)
        # factor 1.21 applied to the size measured at 1.10: 10 % too high
        self.assertAlmostEqual(errors[0], 0.10, places=9)

    def test_one_control_size_cannot_be_checked(self):
        self.assertEqual(fm.check_factor(synth("cpu_task", [2000]), 2e-9, 1.5), [])


class Files(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)

    def path(self, name, content=None):
        p = os.path.join(self.dir.name, name)
        if content is not None:
            with open(p, "w", newline="") as f:
                f.write(content)
        return p

    def test_read_measurements_finds_columns_by_name(self):
        p = self.path("m.csv", "run,workload,size,runtime_s,max_rss_kb,extra\r\n"
                               "1,cpu_task,4000000,2.61,2376,x\r\n")
        self.assertEqual(fm.read_measurements(p), [fm.Run("cpu_task", 4000000, 2.61, 2376.0)])

    def test_read_measurements_refuses_a_missing_column(self):
        p = self.path("m.csv", "workload,size,run,runtime_s\ncpu_task,1,1,2.0\n")
        with self.assertRaisesRegex(ValueError, "max_rss_kb"):
            fm.read_measurements(p)

    def test_read_measurements_refuses_a_runtime_of_zero(self):
        # log(0) does not exist: a run too short for /usr/bin/time cannot be used
        p = self.path("m.csv", "workload,size,run,runtime_s,max_rss_kb\ncpu_task,10,1,0.00,2000\n")
        with self.assertRaisesRegex(ValueError, "line 2"):
            fm.read_measurements(p)

    def test_read_measurements_refuses_a_size_that_is_not_a_positive_integer(self):
        for bad in ("0", "-5", "1.5", "abc"):
            p = self.path("m.csv", "workload,size,run,runtime_s,max_rss_kb\ncpu_task,%s,1,1.0,2000\n" % bad)
            with self.assertRaisesRegex(ValueError, "line 2"):
                fm.read_measurements(p)

    def test_read_cpu_model_returns_the_model_name_of_lscpu(self):
        p = self.path("cpu.txt", "Architecture:      x86_64\n"
                                 "Model name:        Intel(R) Xeon(R) CPU @ 2.20GHz\n"
                                 "CPU family:        6\n")
        self.assertEqual(fm.read_cpu_model(p), CPU)

    def test_read_cpu_model_refuses_a_file_without_model_name(self):
        with self.assertRaisesRegex(ValueError, "Model name"):
            fm.read_cpu_model(self.path("cpu.txt", "Architecture: x86_64\n"))

    def test_read_cpu_platform_names_family_6_model_79_as_broadwell(self):
        p = self.path("cpu.txt", "Vendor ID:         GenuineIntel\n"
                                 "Model name:        Intel(R) Xeon(R) CPU @ 2.20GHz\n"
                                 "CPU family:        6\n"
                                 "Model:             79\n")
        self.assertEqual(fm.read_cpu_platform(p), "family 6, model 79 = Intel Broadwell")

    def test_read_cpu_platform_of_an_unknown_cpu_gives_only_the_numbers(self):
        p = self.path("cpu.txt", "Vendor ID:         GenuineIntel\n"
                                 "CPU family:        6\n"
                                 "Model:             1\n")
        self.assertEqual(fm.read_cpu_platform(p), "family 6, model 1")

    def test_write_model_refuses_a_comma_inside_a_value(self):
        rows = fm.build_model({"big": synth("cpu_task", [1000, 2000, 4000])},
                              {"big": ("Some CPU, 2.2GHz", "declared")})
        out = self.path("model.csv")
        with self.assertRaisesRegex(ValueError, "comma"):
            fm.write_model(out, rows)
        self.assertFalse(os.path.exists(out))

    def test_write_model_has_the_exact_header_and_unix_line_endings(self):
        rows = fm.build_model({"big": synth("cpu_task", [1000, 2000, 4000])}, cpus("big"))
        out = self.path("model.csv")
        fm.write_model(out, rows)
        with open(out, "rb") as f:
            raw = f.read()
        self.assertNotIn(b"\r", raw)
        self.assertNotIn(b'"', raw)
        self.assertEqual(raw.split(b"\n")[0].decode(),
                         "workload,machine_type,a,b,factor,c,k,size_min,size_max,"
                         "factor_source,cpu_model,cpu_source")
        self.assertTrue(raw.endswith(b"\n"))

    def test_write_model_keeps_enough_digits_to_predict(self):
        rows = fm.build_model({"big": synth("cpu_task", [1000, 2000, 4000])}, cpus("big"))
        out = self.path("model.csv")
        fm.write_model(out, rows)
        with open(out, newline="") as f:
            r = next(csv.DictReader(f))
        runtime = float(r["factor"]) * float(r["a"]) * 3000 ** float(r["b"])
        rss = float(r["c"]) * 3000 ** float(r["k"])
        self.assertAlmostEqual(runtime / (2e-9 * 3000 ** 1.5), 1.0, places=6)
        self.assertAlmostEqual(rss / (8.0 * 3000), 1.0, places=6)
        self.assertEqual((r["size_min"], r["size_max"]), ("1000", "4000"))


class RealData(unittest.TestCase):
    """The script on the real measurements of data/ (written to a temporary file)."""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.TemporaryDirectory()
        cls.out = os.path.join(cls.dir.name, "model.csv")
        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            cls.code = fm.main(["--out", cls.out])
        cls.report = buffer.getvalue()
        with open(cls.out, newline="") as f:
            cls.rows = list(csv.DictReader(f))

    @classmethod
    def tearDownClass(cls):
        cls.dir.cleanup()

    def predict(self, workload, machine_type, size):
        r = row_of(self.rows, workload, machine_type)
        return float(r["factor"]) * float(r["a"]) * size ** float(r["b"])

    def test_it_succeeds_and_writes_four_workloads_on_three_machine_types(self):
        self.assertEqual(self.code, 0)
        self.assertEqual(len(self.rows), 12)

    def test_exponents_match_the_complexity_of_each_workload(self):
        expected = {"cpu_task": (1.3, 1.5), "memory_task": (0.95, 1.05),
                    "matrix_task": (2.9, 3.2), "sort_task": (1.0, 1.1)}
        for workload, (low, high) in expected.items():
            b = float(row_of(self.rows, workload, "e2-standard-2")["b"])
            self.assertTrue(low < b < high, "%s: b = %s" % (workload, b))

    def test_predictions_are_close_to_measurements_never_used_for_a_and_b(self):
        # control points of e2-highcpu-4 (mean of the three runs)
        self.assertAlmostEqual(self.predict("cpu_task", "e2-highcpu-4", 16000000) / 19.23, 1.0, delta=0.03)
        self.assertAlmostEqual(self.predict("matrix_task", "e2-highcpu-4", 1500) / 6.08, 1.0, delta=0.05)
        self.assertAlmostEqual(self.predict("memory_task", "e2-highmem-2", 2048) / 3.543, 1.0, delta=0.05)

    def test_memory_task_rss_is_its_size_in_megabytes(self):
        r = row_of(self.rows, "memory_task", "e2-highcpu-4")
        rss_kb = float(r["c"]) * 2048 ** float(r["k"])
        self.assertAlmostEqual(rss_kb / 2102164, 1.0, delta=0.01)

    def test_sort_task_reference_is_highmem_and_standard_is_measured(self):
        # Until the GCP session of 16/11/2026 sort_task had never run on
        # e2-standard-2 and its factor was 1.0 "assumed". Two sizes were
        # measured that day (run 20261116-profile-e2std2): 2 % faster than
        # the reference machine.
        self.assertEqual(row_of(self.rows, "sort_task", "e2-highmem-2")["factor_source"], "reference")
        self.assertEqual(row_of(self.rows, "sort_task", "e2-highcpu-4")["factor_source"], "measured")
        r = row_of(self.rows, "sort_task", "e2-standard-2")
        self.assertEqual(r["factor_source"], "measured")
        self.assertTrue(0.96 < float(r["factor"]) < 0.99, r["factor"])
        self.assertNotIn("assumed", [row["factor_source"] for row in self.rows])
        self.assertNotIn("WARNING", self.report)

    def test_cpu_of_the_lost_vm_is_declared_not_verified(self):
        for r in self.rows:
            self.assertEqual(r["cpu_model"], CPU)
            expected = "declared" if r["machine_type"] == "e2-standard-2" else "lscpu"
            self.assertEqual(r["cpu_source"], expected)

    def test_report_says_the_model_is_only_valid_on_this_cpu(self):
        self.assertIn(CPU, self.report)
        self.assertIn("Intel Broadwell", self.report)
        self.assertIn("only valid on this CPU", self.report)

    def test_leave_one_size_out_error_stays_under_five_percent(self):
        for workload in ("cpu_task", "memory_task", "matrix_task", "sort_task"):
            runs = [r for r in fm.read_measurements(os.path.join(
                ROOT, "data", "profil_profil-hmem2.csv" if workload == "sort_task"
                else "profil_e2-standard-2.csv")) if r.workload == workload]
            checks = fm.leave_one_size_out([r.size for r in runs], [r.runtime_s for r in runs])
            self.assertLess(max(abs(c.error) for c in checks), 0.05, workload)


class MainErrors(unittest.TestCase):
    def test_a_missing_file_gives_exit_code_1_and_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "model.csv")
            profiles = [("e2-standard-2", os.path.join(d, "missing.csv"), None, CPU)]
            err = io.StringIO()
            with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
                code = fm.main(["--out", out], profiles=profiles)
            self.assertEqual(code, 1)
            self.assertIn("Error:", err.getvalue())
            self.assertFalse(os.path.exists(out))

    def test_several_files_of_one_machine_type_are_merged(self):
        with tempfile.TemporaryDirectory() as d:
            header = "workload,size,run,runtime_s,max_rss_kb\n"
            a = os.path.join(d, "a.csv")
            b = os.path.join(d, "b.csv")
            with open(a, "w") as f:
                f.write(header + "".join("cpu_task,%d,1,%r,2000\n" % (s, 1e-3 * s) for s in (10, 20)))
            with open(b, "w") as f:
                f.write(header + "cpu_task,40,1,0.04,2000\n")
            out = os.path.join(d, "model.csv")
            with contextlib.redirect_stdout(io.StringIO()):
                code = fm.main(["--out", out], profiles=[("t", a, None, CPU), ("t", b, None, CPU)])
            self.assertEqual(code, 0)
            with open(out, newline="") as f:
                r = next(csv.DictReader(f))
            self.assertEqual((r["size_min"], r["size_max"]), ("10", "40"))
            self.assertTrue(math.isclose(float(r["b"]), 1.0, abs_tol=1e-9))


if __name__ == "__main__":
    unittest.main()

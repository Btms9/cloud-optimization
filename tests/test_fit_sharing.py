# test_fit_sharing.py - tests of scripts/fit_sharing.py (calibration of the
# CPU sharing used by the greedy scheduler, step 2).
#
# Run, from the project folder:  python -m unittest tests/test_fit_sharing.py
#
# No VM and no GCP access are needed. The finish-time cases are the same as
# in scheduler/greedy_test.go: the Go scheduler and this script must agree.
import contextlib
import importlib.util
import io
import os
import re
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "fit_sharing", os.path.join(ROOT, "scripts", "fit_sharing.py"))
fs = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fs)

INTEL = "Intel(R) Xeon(R) CPU @ 2.20GHz"


class FinishTimes(unittest.TestCase):
    def assertTimes(self, got, expected):
        self.assertEqual(len(got), len(expected))
        for g, e in zip(got, expected):
            self.assertAlmostEqual(g, e, places=9)

    def test_no_task_gives_no_time(self):
        self.assertEqual(fs.finish_times([], 2, 1.0), [])
        self.assertEqual(fs.finish_time([], 2, 1.0), 0.0)

    def test_a_task_alone_runs_at_full_speed(self):
        self.assertTimes(fs.finish_times([7.5], 2, 0.6), [7.5])

    def test_capacity_is_never_below_one_task(self):
        # 1 vCPU with efficiency 0.5 still runs one task at full speed,
        # and two tasks at half speed each
        self.assertTimes(fs.finish_times([5], 1, 0.5), [5])
        self.assertTimes(fs.finish_times([5, 5], 1, 0.5), [10, 10])

    def test_as_many_tasks_as_ideal_vcpus_do_not_slow_down(self):
        self.assertTimes(fs.finish_times([10, 10], 2, 1.0), [10, 10])

    def test_short_task_delays_the_long_one_while_it_runs(self):
        # capacity 1: both at half speed until the short one ends (2 / 0.5 = 4 s),
        # then the long one finishes its last 8 s alone
        self.assertTimes(fs.finish_times([2, 10], 2, 0.5), [4, 12])

    def test_times_are_returned_in_the_order_of_the_tasks(self):
        self.assertTimes(fs.finish_times([10, 2], 2, 0.5), [12, 4])

    def test_three_tasks_on_two_ideal_vcpus(self):
        # 3 tasks share 2: speed 2/3 until the first ends (1.5 s), then full speed
        self.assertTimes(fs.finish_times([1, 2, 3], 2, 1.0), [1.5, 2.5, 3.5])

    def test_finish_time_is_the_end_of_the_last_task(self):
        self.assertAlmostEqual(fs.finish_time([10, 2], 2, 0.5), 12)

    def test_closed_form_is_the_formula_of_the_lpt_scheduler(self):
        self.assertAlmostEqual(fs.closed_form([2, 10], 2, 1.0), 10)   # the longest task
        self.assertAlmostEqual(fs.closed_form([8, 8, 8], 2, 1.0), 12)  # sum / vCPUs
        self.assertAlmostEqual(fs.closed_form([8, 8, 8], 2, 0.5), 24)

    def test_sharing_never_finishes_before_the_closed_form(self):
        for runtimes in ([2, 10], [1, 2, 3], [8, 8, 8], [1, 1, 1, 1, 30], [5]):
            for e in (1.0, 0.6):
                self.assertGreaterEqual(fs.finish_time(runtimes, 2, e) + 1e-9,
                                        fs.closed_form(runtimes, 2, e))


def synthetic(efficiency):
    """VM-runs whose measured times follow the sharing model exactly."""
    cases = [("a", 2, [1, 4, 9]), ("b", 4, [2, 2, 3, 5, 8, 13, 21]), ("c", 2, [6, 6]),
             ("d", 4, [30]), ("e", 2, [1, 1, 2, 3, 20])]
    return [fs.Observation(label, vcpus, runtimes,
                           fs.finish_times(runtimes, vcpus, efficiency))
            for label, vcpus, runtimes in cases]


class Calibrate(unittest.TestCase):
    def test_recovers_the_efficiency_that_produced_the_measurements(self):
        best, error = fs.calibrate(synthetic(0.60), fs.finish_time)
        self.assertAlmostEqual(best, 0.60, places=9)
        self.assertAlmostEqual(error, 0.0, places=9)

    def test_ideal_vcpus_are_found_when_the_measurements_are_ideal(self):
        best, _ = fs.calibrate(synthetic(1.0), fs.finish_time)
        self.assertAlmostEqual(best, 1.0, places=9)

    def test_rms_error_is_zero_for_the_right_efficiency_only(self):
        observations = synthetic(0.60)
        self.assertAlmostEqual(fs.rms_error(observations, 0.60, fs.finish_time), 0.0, places=9)
        self.assertGreater(fs.rms_error(observations, 1.0, fs.finish_time), 0.05)

    def test_leave_one_out_predicts_each_run_from_the_others(self):
        checks = fs.leave_one_out(synthetic(0.60), fs.finish_time)
        self.assertEqual([c.label for c in checks], ["a", "b", "c", "d", "e"])
        for c in checks:
            self.assertAlmostEqual(c.efficiency, 0.60, places=9)
            self.assertAlmostEqual(c.error, 0.0, places=9)

    def test_no_observation_is_refused(self):
        with self.assertRaisesRegex(ValueError, "no VM-run"):
            fs.calibrate([], fs.finish_time)


RESULTS_HEADER = ("run_id,mode,task_id,workload,size,vm,zone,machine_type,host,status,exit_code,"
                  "attempts,submit_ts,start_ts,end_ts,runtime_s,max_rss_kb,wall_s\n")


class LoadObservations(unittest.TestCase):
    """A small fake project: one run with three VMs."""

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.root = self.dir.name
        os.makedirs(os.path.join(self.root, "run1"))
        self.write("run1/machines.csv",
                   "vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\n"
                   "vm1,z,type-a,2,8,0.1\nvm2,z,type-b,4,4,0.1\nvm3,z,type-a,2,8,0.1\n")

        def row(task, size, vm, machine_type, status, start, end):
            return "run1,concurrent,%s,w,%d,%s,z,%s,%s,%s,0,1,99,%s,%s,1.0,2000,5.0\n" % (
                task, size, vm, machine_type, vm, status, start, end)
        self.write("run1/results.csv", RESULTS_HEADER
                   + row("T1", 10, "vm1", "type-a", "OK", 100.5, 104.5)
                   + row("T2", 20, "vm1", "type-a", "OK", 100.0, 112.0)
                   + row("T3", 10, "vm2", "type-b", "OK", 100.0, 103.0)
                   + row("T4", 10, "vm3", "type-a", "OK", 100.0, 103.0)
                   + row("T5", 10, "vm3", "type-a", "FAILED", 100.0, 101.0))
        self.write("cpu_vm1.txt", "Model name:   %s\n" % INTEL)
        self.write("cpu_vm2.txt", "Model name:   AMD EPYC 7B12\n")
        self.write("cpu_vm3.txt", "Model name:   %s\n" % INTEL)
        # runtime = factor * a * size^b: 0.5 * size on type-a, 0.6 * size on type-b
        self.model = self.write(
            "model.csv",
            "workload,machine_type,a,b,factor,c,k,size_min,size_max,factor_source,cpu_model,cpu_source\n"
            "w,type-a,0.5,1,1.0,1,1,1,100,reference,%s,lscpu\n"
            "w,type-b,0.5,1,1.2,1,1,1,100,measured,%s,lscpu\n" % (INTEL, INTEL))
        self.runs = [("run1", "cpu_{vm}.txt")]

    def write(self, name, content):
        path = os.path.join(self.root, name)
        with open(path, "w", newline="") as f:
            f.write(content)
        return path

    def load(self):
        return fs.load_observations(self.root, self.runs, fs.read_model(self.model))

    def test_read_model_gives_the_runtime_of_a_task_alone(self):
        model = fs.read_model(self.model)
        self.assertAlmostEqual(model.runtime("w", "type-a", 10), 5.0)
        self.assertAlmostEqual(model.runtime("w", "type-b", 10), 6.0)
        self.assertEqual(model.cpu_model, INTEL)

    def test_a_vm_with_the_cpu_of_the_model_becomes_one_observation(self):
        observations, _ = self.load()
        self.assertEqual(len(observations), 1)
        obs = observations[0]
        self.assertEqual(obs.label, "run1 vm1")
        self.assertEqual(obs.vcpus, 2)
        self.assertEqual(obs.runtimes, [5.0, 10.0])
        # times are counted from the first start of the VM (100.0), not per task
        self.assertEqual(obs.ends, [4.5, 12.0])

    def test_a_vm_with_another_cpu_is_skipped_with_the_reason(self):
        _, skipped = self.load()
        self.assertIn(("run1 vm2", "CPU is AMD EPYC 7B12, not the CPU of the model"), skipped)

    def test_a_vm_with_a_failed_task_is_skipped_with_the_reason(self):
        _, skipped = self.load()
        self.assertIn(("run1 vm3", "task T5 is FAILED"), skipped)

    def test_a_missing_cpu_description_is_skipped_with_the_reason(self):
        os.remove(os.path.join(self.root, "cpu_vm1.txt"))
        observations, skipped = self.load()
        self.assertEqual(observations, [])
        self.assertIn(("run1 vm1", "no CPU description (cpu_vm1.txt)"), skipped)

    def test_a_queue_run_is_skipped(self):
        path = os.path.join(self.root, "run1", "results.csv")
        with open(path) as f:
            content = f.read().replace(",concurrent,", ",queue,")
        self.write("run1/results.csv", content)
        observations, skipped = self.load()
        self.assertEqual(observations, [])
        self.assertIn(("run1 vm1", "mode is queue: the tasks did not run together"), skipped)


class RealData(unittest.TestCase):
    """The script on the runs of results/runs/ and data/model.csv."""

    @classmethod
    def setUpClass(cls):
        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            cls.code = fs.main([])
        cls.report = buffer.getvalue()

    def test_it_succeeds(self):
        self.assertEqual(self.code, 0)

    def test_ten_vm_runs_are_used_and_the_amd_ones_are_skipped(self):
        self.assertIn("10 VM-runs used", self.report)
        self.assertIn("5 skipped", self.report)
        self.assertIn("AMD EPYC 7B12", self.report)

    def test_calibrated_efficiency_is_well_below_ideal_vcpus(self):
        match = re.search(r"vCPU efficiency: (\d\.\d\d)", self.report)
        self.assertIsNotNone(match, self.report)
        self.assertTrue(0.55 <= float(match.group(1)) <= 0.70, match.group(1))

    def test_sharing_formula_beats_the_ideal_vcpu_formula(self):
        observations, _ = fs.load_observations(ROOT, fs.RUNS, fs.read_model(
            os.path.join(ROOT, "data", "model.csv")))
        best, error = fs.calibrate(observations, fs.finish_time)
        self.assertLess(error, 0.08)
        self.assertGreater(fs.rms_error(observations, 1.0, fs.closed_form), 0.20)
        self.assertLess(error, fs.calibrate(observations, fs.closed_form)[1])


class MainErrors(unittest.TestCase):
    def test_a_missing_model_gives_exit_code_1(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            code = fs.main(["--model", os.path.join(ROOT, "data", "no_such_model.csv")])
        self.assertEqual(code, 1)
        self.assertIn("Error:", err.getvalue())


if __name__ == "__main__":
    unittest.main()

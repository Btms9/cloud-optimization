# test_compare_seeds.py - tests of scripts/compare_seeds.py (Round-Robin
# against greedy on many random lots, predictions only).
#
# Run, from the project folder:  python -m unittest tests/test_compare_seeds.py
#
# No VM and no GCP access are needed. The script runs the Go schedulers and
# predict_makespan: the tests that need Go are skipped where Go is not
# installed (on Youssef's PC: Windows has no Go, WSL has it). The other tests
# give the script the text that predict_makespan prints, or a fake "run".
import contextlib
import importlib.util
import io
import os
import random
import shutil
import sys
import tempfile
import unittest
from collections import namedtuple

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
_spec = importlib.util.spec_from_file_location(
    "compare_seeds", os.path.join(ROOT, "scripts", "compare_seeds.py"))
cs = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cs)
import gen_tasks as gt  # noqa: E402  (scripts/ was added to the path above)

# What predict_makespan prints (copied from real runs)
TWO = """== /tmp/x/rr.csv: 18 tasks on 3 VMs
  cloudopt-vm1   e2-standard-2  2 vCPU |   6 tasks | alone: longest   12.7s, total    29.5s | finish   24.5s | memory   999 of  6093 MB
  cloudopt-vm2   e2-highcpu-4   4 vCPU |   6 tasks | alone: longest    9.4s, total    32.3s | finish   14.0s | memory   977 of  2611 MB
  cloudopt-vm3   e2-highmem-2   2 vCPU |   6 tasks | alone: longest   14.3s, total    32.8s | finish   27.6s | memory  1780 of 13056 MB
  Estimated makespan: 27.6s, set by cloudopt-vm3 (vCPU efficiency 0.63)
  Lower bound for any allocation of these tasks with this model: 18.1s
== /tmp/x/greedy.csv: 18 tasks on 3 VMs
  cloudopt-vm1   e2-standard-2  2 vCPU |   5 tasks | alone: longest   13.9s, total    23.2s | finish   20.2s | memory  2694 of  6093 MB
  cloudopt-vm2   e2-highcpu-4   4 vCPU |   9 tasks | alone: longest    9.4s, total    49.3s | finish   20.3s | memory   216 of  2611 MB
  cloudopt-vm3   e2-highmem-2   2 vCPU |   4 tasks | alone: longest   13.1s, total    23.1s | finish   19.9s | memory   845 of 13056 MB
  Estimated makespan: 20.3s, set by cloudopt-vm2 (vCPU efficiency 0.63)
  Lower bound for any allocation of these tasks with this model: 18.1s
Comparison (same model and same estimator for all):
  /tmp/x/rr.csv         27.6s
  /tmp/x/greedy.csv     20.3s  -26.6 % vs /tmp/x/rr.csv
Model measured on: Intel(R) Xeon(R) CPU @ 2.20GHz - these estimates are only valid on this CPU
"""
WARNING_VM1 = ("  Warning: cloudopt-vm1: predicted memory 7443 MB is above its limit of 6093 MB "
               "(85 % of its memory after 1 GB kept for the system): tasks may be killed, "
               "and the estimate assumes they all run\n")
WARNING_VM2 = WARNING_VM1.replace("vm1", "vm2").replace("7443", "9505").replace("6093", "2611")
RR_OVER = TWO.replace("== /tmp/x/greedy.csv", WARNING_VM1 + WARNING_VM2 + "== /tmp/x/greedy.csv")
GREEDY_SLOWER = TWO.replace("-26.6 % vs", "+3.8 % vs")
ONE = TWO.split("== /tmp/x/greedy.csv")[0] + TWO[TWO.index("Model measured on"):]


class ParseReport(unittest.TestCase):
    def test_makespan_of_each_allocation_and_the_gain_of_the_second(self):
        evaluations, gains = cs.parse_report(TWO)
        self.assertEqual(evaluations, [cs.Evaluation(27.6, []), cs.Evaluation(20.3, [])])
        self.assertEqual(gains, [-26.6])

    def test_memory_warnings_belong_to_their_allocation(self):
        evaluations, _ = cs.parse_report(RR_OVER)
        self.assertEqual(evaluations[0].over, [("cloudopt-vm1", 7443, 6093),
                                               ("cloudopt-vm2", 9505, 2611)])
        self.assertEqual(evaluations[1].over, [])

    def test_a_slower_second_allocation_has_a_positive_gain(self):
        self.assertEqual(cs.parse_report(GREEDY_SLOWER)[1], [3.8])

    def test_one_allocation_has_no_gain(self):
        evaluations, gains = cs.parse_report(ONE)
        self.assertEqual((len(evaluations), gains), (1, []))
        self.assertEqual(evaluations[0].makespan, 27.6)

    def test_a_report_without_makespan_is_refused(self):
        for text in ("", "Error: something\n",
                     TWO.replace("  Estimated makespan: 20.3s", "  Estimated: 20.3s")):
            with self.assertRaises(ValueError):
                cs.parse_report(text)


L = cs.Lot
LOTS = [
    L(1, 30.0, 20.0, -33.3, None),
    L(2, 20.0, 22.0, 10.0, ("cloudopt-vm2", 5000, 2611)),
    L(3, 25.0, None, None, ("cloudopt-vm2", 3000, 2611)),      # greedy: no allocation
    L(4, 40.0, 40.0, 0.0, None),
    L(5, 10.0, 8.0, -20.0, ("cloudopt-vm1", 7000, 6093)),
]


class Summarize(unittest.TestCase):
    def test_counts(self):
        s = cs.summarize(LOTS)
        self.assertEqual(s.total, 5)
        self.assertEqual(s.no_allocation, [3])
        self.assertEqual(s.over, 3)                 # seeds 2, 3 and 5

    def test_worst_memory_is_the_largest_share_of_the_limit(self):
        # 5000 of 2611 MB (seed 2) is worse than 7000 of 6093 MB (seed 5)
        self.assertEqual(cs.summarize(LOTS).worst_over, LOTS[1])

    def test_all_the_lots_where_greedy_found_an_allocation(self):
        c = cs.summarize(LOTS).compared
        self.assertEqual(c.lots, 4)
        self.assertAlmostEqual(c.mean, (-33.3 + 10.0 + 0.0 - 20.0) / 4)
        self.assertEqual((c.worst, c.best), (LOTS[1], LOTS[0]))
        self.assertAlmostEqual(c.rr, 25.0)
        self.assertAlmostEqual(c.greedy, 22.5)
        self.assertEqual((c.faster, c.equal, c.slower), (2, 1, 1))

    def test_fair_comparison_leaves_out_the_lots_where_round_robin_is_above_the_limit(self):
        f = cs.summarize(LOTS).fair
        self.assertEqual(f.lots, 2)                 # seeds 1 and 4
        self.assertAlmostEqual(f.mean, (-33.3 + 0.0) / 2)
        self.assertEqual((f.worst, f.best), (LOTS[3], LOTS[0]))
        self.assertEqual((f.faster, f.equal, f.slower), (1, 1, 0))

    def test_no_lot_to_compare_gives_no_statistics(self):
        s = cs.summarize([LOTS[2]])
        self.assertEqual((s.total, s.over, s.compared, s.fair), (1, 1, None, None))
        self.assertIsNone(cs.summarize([LOTS[0]]).worst_over)

    def test_summary_text(self):
        text = cs.format_summary(cs.summarize(LOTS))
        self.assertIn("Round-Robin above the memory limit of a VM: 3 of 5 lots", text)
        self.assertIn("seed 2: 5000 MB on cloudopt-vm2, limit 2611 MB", text)
        self.assertIn("greedy found no allocation: 1 of 5 lots (seed 3)", text)
        self.assertRegex(text, r"mean +-10\.8 %")
        self.assertRegex(text, r"worst +\+10\.0 % \(seed 2\)")
        self.assertRegex(text, r"best +-33\.3 % \(seed 1\)")
        self.assertIn("faster in 2 lots, equal in 1, slower in 1", text)
        self.assertRegex(text, r"mean +-16\.6 %")       # the fair comparison
        self.assertRegex(text, r"worst +\+?-?0\.0 % \(seed 4\)")

    def test_summary_text_when_nothing_can_be_compared(self):
        text = cs.format_summary(cs.summarize([LOTS[2]]))
        self.assertIn("greedy found no allocation: 1 of 1 lots (seed 3)", text)
        self.assertIn("no lot to compare", text)


Done = namedtuple("Done", "returncode stdout stderr")
MODEL_HEADER = ("workload,machine_type,a,b,factor,c,k,size_min,size_max,"
                "factor_source,cpu_model,cpu_source\n")


class Evaluate(unittest.TestCase):
    """evaluate() with a fake "run": no Go program is started."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = self._tmp.name
        self.model = os.path.join(self.dir, "model.csv")
        self.machines = os.path.join(self.dir, "machines.csv")
        with open(self.model, "w", newline="") as f:
            f.write(MODEL_HEADER + "lin,ref,0.25,1,1.0,10,1,2,100,reference,X,lscpu\n")
        with open(self.machines, "w", newline="") as f:
            f.write("vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\nvmA,z,ref,2,8,0.1\n")
        workloads, model = gt.load_model(self.model, self.machines)
        self.sizes = [gt.allowed_sizes("lin", model["lin"], 1.0, 15.0)]
        self.tools = {"rr": "RR", "greedy": "GREEDY", "predict": "PREDICT"}
        self.calls = []

    def fake(self, greedy=Done(0, "", ""), rr=Done(0, "", ""), report=TWO, predict_code=0):
        def run(argv):
            self.calls.append(list(argv))
            if argv[0] == "RR":
                return rr
            if argv[0] == "GREEDY":
                return greedy
            return Done(predict_code, report, "boom" if predict_code else "")
        return run

    def evaluate(self, run, seed=7):
        return cs.evaluate(seed, self.sizes, 5, self.tools, self.machines, self.model,
                           self.dir, run)

    def test_a_lot_goes_through_both_schedulers_and_predict_makespan(self):
        lot = self.evaluate(self.fake())
        self.assertEqual(lot, cs.Lot(7, 27.6, 20.3, -26.6, None))
        tasks, rr, greedy = self.calls[0][1], self.calls[0][3], self.calls[1][4]
        self.assertEqual(self.calls, [
            ["RR", tasks, self.machines, rr],
            ["GREEDY", tasks, self.machines, self.model, greedy],
            ["PREDICT", self.machines, self.model, rr, greedy],
        ])
        self.assertEqual(len(set([tasks, rr, greedy])), 3)

    def test_the_lot_is_the_one_the_generator_writes_for_that_seed(self):
        self.evaluate(self.fake(), seed=7)
        expected = gt.generate(self.sizes, 5, random.Random(7))
        self.assertEqual([(t["task_id"], t["workload"], int(t["size"]))
                          for t in gt.read_csv(self.calls[0][1])], expected)

    def test_round_robin_above_the_limit_keeps_the_worst_vm(self):
        lot = self.evaluate(self.fake(report=RR_OVER))
        self.assertEqual(lot.over, ("cloudopt-vm2", 9505, 2611))
        self.assertEqual((lot.rr, lot.greedy), (27.6, 20.3))

    def test_greedy_without_allocation_still_evaluates_round_robin(self):
        refused = Done(1, "", "Error: task T2 (memory_task 2724, predicted memory 2728 MB) "
                              "does not fit: no VM has enough memory left\n")
        lot = self.evaluate(self.fake(greedy=refused, report=ONE))
        self.assertEqual(lot, cs.Lot(7, 27.6, None, None, None))
        self.assertEqual(len(self.calls[2]), 4)     # predict_makespan got one allocation

    def test_any_other_failure_stops_everything(self):
        cases = [
            ("greedy fails for another reason",
             self.fake(greedy=Done(1, "", "Error: bad file\n"), report=ONE)),
            ("round robin fails", self.fake(rr=Done(1, "", "Error: x\n"))),
            ("predict_makespan fails", self.fake(predict_code=1)),
            ("no gain in the report", self.fake(report=ONE)),
            ("greedy above the memory limit", self.fake(report=TWO.replace(
                "Comparison (", WARNING_VM2 + "Comparison ("))),
        ]
        for name, run in cases:
            with self.subTest(name):
                with self.assertRaises(RuntimeError):
                    self.evaluate(run)


@unittest.skipUnless(shutil.which("go"), "needs Go (on this PC: run the tests inside WSL)")
class WithGo(unittest.TestCase):
    """The whole script with the real schedulers, model and machines."""

    @classmethod
    def setUpClass(cls):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            cls.code = cs.main(["--seeds", "3", "--count", "18",
                                "--workloads", "cpu_task,memory_task,matrix_task"])
        cls.out, cls.err = out.getvalue(), err.getvalue()

    def test_it_succeeds(self):
        self.assertEqual((self.code, self.err), (0, ""))

    def test_seed_1_is_the_lot_of_the_gcp_procedure(self):
        # data/tasks_gen_s1.csv: 27.6 s, 20.3 s, -26.6 % with predict_makespan
        self.assertRegex(self.out, r"(?m)^ +1 +27\.6 s +20\.3 s +-26\.6 % ")

    def test_one_line_per_seed_and_a_summary(self):
        self.assertRegex(self.out, r"(?m)^ +3 +\d+\.\d s ")
        self.assertNotRegex(self.out, r"(?m)^ +4 +\d")
        self.assertIn("Seeds 1 to 3, 18 tasks", self.out)
        self.assertIn("at most 1024 MB per task", self.out)
        self.assertIn("Round-Robin above the memory limit of a VM:", self.out)
        self.assertRegex(self.out, r"mean +[+-]\d+\.\d %")

    def test_nothing_is_left_in_the_project(self):
        self.assertEqual(os.listdir(os.path.join(ROOT, "data")).count("tasks_1.csv"), 0)


class MainErrors(unittest.TestCase):
    def run_main(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cs.main(list(args))
        return code, err.getvalue()

    def test_invalid_requests_give_exit_code_1(self):
        for args, expected in ((["--seeds", "0"], "seeds"),
                               (["--count", "0"], "count"),
                               (["--max-task-mb", "-1"], "max-task-mb"),
                               (["--model", "no_such_model.csv"], "no_such_model.csv")):
            with self.subTest(args):
                code, err = self.run_main(*args)
                self.assertEqual(code, 1)
                self.assertIn("Error:", err)
                self.assertIn(expected, err)

    @unittest.skipIf(shutil.which("go"), "Go is installed here")
    def test_without_go_it_says_where_to_run_it(self):
        code, err = self.run_main("--seeds", "2")
        self.assertEqual(code, 1)
        self.assertIn("Go", err)
        self.assertIn("WSL", err)


if __name__ == "__main__":
    unittest.main()

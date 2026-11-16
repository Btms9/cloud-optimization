# test_gen_tasks.py - tests of scripts/gen_tasks.py (seeded random task
# generator, step 4).
#
# Run, from the project folder:  python -m unittest tests/test_gen_tasks.py
#
# No VM and no GCP access are needed. Most tests use a small model whose
# numbers are exact in binary, so the expected sizes are computed by hand:
#   lin:    runtime = 0.25 * size      factor 1 on "ref", 1.25 on "slow"
#   square: runtime = size^2 / 16      factor 1 on both
# With tasks of 1 to 15 s: lin 4..48 (0.25*4 = 1 s on ref, 0.25*48*1.25 = 15 s
# on slow), square 4..15 (16/16 = 1 s, 225/16 = 14.06 s, 256/16 = 16 s).
import contextlib
import importlib.util
import io
import os
import random
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "gen_tasks", os.path.join(ROOT, "scripts", "gen_tasks.py"))
gt = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(gt)

MODEL_HEADER = ("workload,machine_type,a,b,factor,c,k,size_min,size_max,"
                "factor_source,cpu_model,cpu_source\n")
MODEL = MODEL_HEADER + (
    "lin,ref,0.25,1,1.0,10,1,2,100,reference,X,lscpu\n"
    "lin,slow,0.25,1,1.25,10,1,2,100,measured,X,lscpu\n"
    "square,ref,0.0625,2,1.0,1,1,1,50,reference,X,lscpu\n"
    "square,slow,0.0625,2,1.0,1,1,1,50,measured,X,lscpu\n")
GUESS = ("guess,ref,0.25,1,1.0,10,1,2,100,reference,X,lscpu\n"
         "guess,slow,0.25,1,1.0,10,1,2,100,assumed,X,lscpu\n")
MACHINES = ("vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\n"
            "vmA,z,ref,2,8,0.1\n"
            "vmB,z,slow,2,8,0.1\n"
            "vmC,z,ref,2,8,0.1\n")

# random.Random(1).random(), ten times: the sequence Python guarantees for seed 1
SEED_1 = [0.13436424411240122, 0.8474337369372327, 0.763774618976614,
          0.2550690257394217, 0.49543508709194095, 0.4494910647887381,
          0.651592972722763, 0.7887233511355132, 0.0938595867742349,
          0.02834747652200631]


class Scripted:
    """A random generator that returns the numbers it was given."""

    def __init__(self, numbers):
        self.numbers = list(numbers)

    def random(self):
        return self.numbers.pop(0)


class Folder(unittest.TestCase):
    """A temporary folder with a model and a machine file."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = self._tmp.name
        self.model = self.write("model.csv", MODEL)
        self.machines = self.write("machines.csv", MACHINES)

    def write(self, name, text):
        path = os.path.join(self.dir, name)
        with open(path, "w", newline="", encoding="utf-8") as f:
            f.write(text)
        return path

    def sizes(self, min_s=1.0, max_s=15.0):
        workloads, model = gt.load_model(self.model, self.machines)
        return [gt.allowed_sizes(w, model[w], min_s, max_s) for w in workloads]

    def run_main(self, *args):
        """Runs the script. Returns (exit code, stdout, stderr)."""
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = gt.main(["--model", self.model, "--machines", self.machines]
                           + list(args))
        return code, out.getvalue(), err.getvalue()

    def raw(self, path):
        with open(path, "rb") as f:
            return f.read()

    def read_tasks(self, path):
        with open(path, newline="", encoding="utf-8") as f:
            lines = f.read().split("\n")
        return [tuple(line.split(",")) for line in lines[1:] if line]


class AllowedSizes(Folder):
    def test_runtime_is_between_the_bounds_on_every_machine_type(self):
        lin, square = self.sizes()
        self.assertEqual((lin.workload, lin.lo, lin.hi), ("lin", 4, 48))
        self.assertEqual((square.workload, square.lo, square.hi), ("square", 4, 15))

    def test_shortest_and_longest_are_the_runtimes_at_the_two_ends(self):
        lin, square = self.sizes()
        self.assertAlmostEqual(lin.shortest, 1.0)      # size 4 on ref
        self.assertAlmostEqual(lin.longest, 15.0)      # size 48 on slow
        self.assertAlmostEqual(square.longest, 225 / 16.0)

    def test_sizes_stay_inside_the_measured_range(self):
        self.model = self.write("model.csv", MODEL_HEADER +
                                "lin,ref,0.25,1,1.0,10,1,10,30,reference,X,lscpu\n"
                                "lin,slow,0.25,1,1.25,10,1,10,30,measured,X,lscpu\n")
        (lin,) = self.sizes()
        self.assertEqual((lin.lo, lin.hi), (10, 30))

    def test_measured_range_is_the_one_common_to_all_machine_types(self):
        self.model = self.write("model.csv", MODEL_HEADER +
                                "lin,ref,0.25,1,1.0,10,1,2,100,reference,X,lscpu\n"
                                "lin,slow,0.25,1,1.25,10,1,5,40,measured,X,lscpu\n")
        (lin,) = self.sizes()
        self.assertEqual((lin.lo, lin.hi), (5, 40))

    def test_other_bounds_give_other_sizes(self):
        lin, square = self.sizes(min_s=2.0, max_s=10.0)
        self.assertEqual((lin.lo, lin.hi), (8, 32))
        self.assertEqual((square.lo, square.hi), (6, 12))  # 36/16 = 2.25, 144/16 = 9

    def test_a_workload_with_no_size_in_the_bounds_is_an_error(self):
        # lin is measured up to size 100 = 25 s on ref: nothing lasts 30 s
        with self.assertRaises(ValueError) as caught:
            self.sizes(min_s=30.0, max_s=40.0)
        self.assertIn("lin", str(caught.exception))

    def test_a_machine_type_missing_from_the_model_is_an_error(self):
        self.machines = self.write("machines.csv", MACHINES + "vmD,z,other,2,8,0.1\n")
        with self.assertRaises(ValueError) as caught:
            gt.load_model(self.model, self.machines)
        self.assertIn("other", str(caught.exception))

    def test_a_machine_file_without_vm_is_an_error(self):
        self.machines = self.write("machines.csv", MACHINES.split("\n")[0] + "\n")
        with self.assertRaises(ValueError):
            gt.load_model(self.model, self.machines)

    def test_files_with_bom_and_windows_line_endings_are_read(self):
        self.model = self.write("model.csv", chr(0xFEFF) + MODEL.replace("\n", "\r\n"))
        self.machines = self.write("machines.csv", MACHINES.replace("\n", "\r\n"))
        lin, square = self.sizes()
        self.assertEqual((lin.lo, lin.hi, square.lo, square.hi), (4, 48, 4, 15))


class Pick(Folder):
    def test_first_number_chooses_the_workload_second_the_runtime(self):
        sizes = self.sizes()
        self.assertEqual(gt.pick(sizes, 0.0, 0.0), ("lin", 4))
        self.assertEqual(gt.pick(sizes, 0.49, 0.0), ("lin", 4))
        self.assertEqual(gt.pick(sizes, 0.5, 0.0), ("square", 4))

    def test_runtime_is_spread_evenly_not_the_size(self):
        sizes = self.sizes()
        # lin: 1 s .. 12 s on ref, middle 6.5 s = size 26
        self.assertEqual(gt.pick(sizes, 0.0, 0.5), ("lin", 26))
        # square: 1 s .. 14.0625 s, middle 7.53125 s = sqrt(120.5) = 10.98 -> 11
        # (the middle SIZE would be 9.5)
        self.assertEqual(gt.pick(sizes, 0.5, 0.5), ("square", 11))

    def test_largest_numbers_give_the_last_workload_and_the_largest_size(self):
        sizes = self.sizes()
        almost_one = 1.0 - 2.0 ** -53   # the largest value random() can return
        self.assertEqual(gt.pick(sizes, almost_one, almost_one), ("square", 15))


class Generate(Folder):
    def test_two_numbers_per_task_workload_first(self):
        tasks = gt.generate(self.sizes(), 2, Scripted([0.0, 0.0, 0.5, 0.5]))
        self.assertEqual(tasks, [("T1", "lin", 4), ("T2", "square", 11)])

    def test_a_seed_means_the_random_sequence_of_python(self):
        from_seed = gt.generate(self.sizes(), 5, random.Random(1))
        self.assertEqual(from_seed, gt.generate(self.sizes(), 5, Scripted(SEED_1)))
        self.assertEqual(len(from_seed), 5)

    def test_every_task_respects_the_bounds_on_every_machine_type(self):
        workloads, model = gt.load_model(self.model, self.machines)
        sizes = self.sizes()
        checked = 0
        for seed in range(50):
            for _, workload, size in gt.generate(sizes, 40, random.Random(seed)):
                self.assertIsInstance(size, int)
                for row in model[workload]:
                    self.assertTrue(int(row["size_min"]) <= size <= int(row["size_max"]))
                    self.assertTrue(1.0 <= gt.runtime(row, size) <= 15.0,
                                    (workload, size, row["machine_type"]))
                checked += 1
        self.assertEqual(checked, 2000)

    def test_every_workload_has_the_same_chance(self):
        tasks = gt.generate(self.sizes(), 400, random.Random(7))
        lin = sum(1 for t in tasks if t[1] == "lin")
        self.assertTrue(160 <= lin <= 240, lin)

    def test_no_task_is_an_error(self):
        with self.assertRaises(ValueError):
            gt.generate(self.sizes(), 0, random.Random(1))


class Main(Folder):
    def test_writes_a_task_file_in_the_format_of_the_schedulers(self):
        out = os.path.join(self.dir, "tasks.csv")
        code, stdout, stderr = self.run_main("--seed", "1", "--count", "5", "--out", out)
        self.assertEqual((code, stderr), (0, ""))
        raw = self.raw(out)
        self.assertTrue(raw.startswith(b"task_id,workload,size\nT1,"), raw[:40])
        self.assertNotIn(b"\r", raw)
        self.assertTrue(raw.endswith(b"\n"))
        self.assertEqual(raw.count(b"\n"), 6)
        workloads, model = gt.load_model(self.model, self.machines)
        expected = gt.generate(self.sizes(), 5, random.Random(1))
        self.assertEqual(self.read_tasks(out),
                         [(t, w, str(s)) for t, w, s in expected])

    def test_same_seed_same_file_other_seed_other_file(self):
        paths = [os.path.join(self.dir, name) for name in ("a.csv", "b.csv", "c.csv")]
        for path, seed in zip(paths, ("3", "3", "4")):
            self.assertEqual(self.run_main("--seed", seed, "--count", "20", "--out", path)[0], 0)
        contents = [self.raw(p) for p in paths]
        self.assertEqual(contents[0], contents[1])
        self.assertNotEqual(contents[0], contents[2])

    def test_report_names_the_seed_the_sizes_and_the_number_of_tasks(self):
        out = os.path.join(self.dir, "tasks.csv")
        _, stdout, _ = self.run_main("--seed", "3", "--count", "20", "--out", out)
        self.assertIn("20 tasks", stdout)
        self.assertIn("seed 3", stdout)
        self.assertIn(out, stdout)
        self.assertRegex(stdout, r"lin +sizes +4 to +48")
        self.assertRegex(stdout, r"square +sizes +4 to +15")
        self.assertIn("memory", stdout.lower())

    def test_an_existing_file_is_not_overwritten(self):
        out = self.write("tasks.csv", "precious\n")
        code, _, stderr = self.run_main("--seed", "1", "--count", "5", "--out", out)
        self.assertEqual(code, 1)
        self.assertIn("already exists", stderr)
        self.assertEqual(self.raw(out), b"precious\n")

    def test_force_overwrites(self):
        out = self.write("tasks.csv", "precious\n")
        code, _, _ = self.run_main("--seed", "1", "--count", "5", "--out", out, "--force")
        self.assertEqual(code, 0)
        self.assertEqual(len(self.read_tasks(out)), 5)

    def test_a_workload_with_an_assumed_factor_is_left_out_and_named(self):
        self.model = self.write("model.csv", MODEL + GUESS)
        out = os.path.join(self.dir, "tasks.csv")
        code, stdout, _ = self.run_main("--seed", "1", "--count", "200", "--out", out)
        self.assertEqual(code, 0)
        self.assertRegex(stdout, r"guess.*assumed")
        self.assertEqual(set(t[1] for t in self.read_tasks(out)), {"lin", "square"})

    def test_an_assumed_workload_asked_by_name_is_used_with_a_warning(self):
        self.model = self.write("model.csv", MODEL + GUESS)
        out = os.path.join(self.dir, "tasks.csv")
        code, stdout, _ = self.run_main("--seed", "1", "--count", "200", "--out", out,
                                        "--workloads", "guess,lin")
        self.assertEqual(code, 0)
        self.assertRegex(stdout, r"Warning: .*guess.*assumed")
        self.assertEqual(set(t[1] for t in self.read_tasks(out)), {"lin", "guess"})

    def test_workloads_keep_the_order_of_the_model_whatever_the_order_asked(self):
        a, b = os.path.join(self.dir, "a.csv"), os.path.join(self.dir, "b.csv")
        self.run_main("--seed", "5", "--count", "30", "--out", a, "--workloads", "lin,square")
        self.run_main("--seed", "5", "--count", "30", "--out", b, "--workloads", "square,lin")
        self.assertEqual(self.raw(a), self.raw(b))
        self.assertEqual(len(self.read_tasks(a)), 30)

    def test_invalid_requests_give_exit_code_1_and_write_nothing(self):
        out = os.path.join(self.dir, "tasks.csv")
        cases = [
            ("unknown workload", ["--workloads", "lin,gpu_task"], "gpu_task"),
            ("no task", ["--count", "0"], "count"),
            ("bounds upside down", ["--min-s", "15", "--max-s", "1"], "min-s"),
            ("negative bound", ["--min-s", "-1"], "min-s"),
            ("negative seed", ["--seed", "-1"], "seed"),
            ("no size in the bounds", ["--min-s", "30", "--max-s", "40"], "lin"),
            ("missing model", ["--model", os.path.join(self.dir, "nope.csv")], "nope.csv"),
            ("missing machines", ["--machines", os.path.join(self.dir, "nope.csv")], "nope.csv"),
        ]
        for name, extra, expected in cases:
            with self.subTest(name):
                args = ["--seed", "1", "--count", "5", "--out", out]
                if "--count" in extra:
                    args = ["--seed", "1", "--out", out]
                code, _, stderr = self.run_main(*(args + extra))
                self.assertEqual(code, 1)
                self.assertIn("Error:", stderr)
                self.assertIn(expected, stderr)
                self.assertFalse(os.path.exists(out))


class RealData(unittest.TestCase):
    """The generator on data/model.csv and data/machines.csv."""

    @classmethod
    def setUpClass(cls):
        cls.model_path = os.path.join(ROOT, "data", "model.csv")
        cls.machines_path = os.path.join(ROOT, "data", "machines.csv")
        cls.workloads, cls.model = gt.load_model(cls.model_path, cls.machines_path)

    def measured(self):
        chosen, _ = gt.choose_workloads(self.workloads, self.model, None)
        return chosen

    def test_cpu_memory_and_matrix_tasks_can_be_generated(self):
        for workload in ("cpu_task", "memory_task", "matrix_task"):
            self.assertIn(workload, self.measured())

    def test_every_task_is_measured_sized_and_lasts_1_to_15_s_on_the_three_vms(self):
        sizes = [gt.allowed_sizes(w, self.model[w], 1.0, 15.0) for w in self.measured()]
        checked = 0
        for seed in range(30):
            for _, workload, size in gt.generate(sizes, 30, random.Random(seed)):
                self.assertEqual(len(self.model[workload]), 3)
                for row in self.model[workload]:
                    self.assertTrue(int(row["size_min"]) <= size <= int(row["size_max"]))
                    self.assertTrue(1.0 <= gt.runtime(row, size) <= 15.0,
                                    (workload, size, row["machine_type"]))
                checked += 1
        self.assertEqual(checked, 900)

    def test_cpu_task_starts_at_its_smallest_measured_size(self):
        cpu = gt.allowed_sizes("cpu_task", self.model["cpu_task"], 1.0, 15.0)
        self.assertEqual(cpu.lo, 4000000)
        self.assertTrue(2.5 < cpu.shortest < 2.8, cpu.shortest)   # not 1 s
        self.assertTrue(14.9 < cpu.longest <= 15.0, cpu.longest)

    def test_allowed_sizes_are_exactly_the_sizes_inside_the_bounds(self):
        for w in self.measured():
            rows = self.model[w]
            s = gt.allowed_sizes(w, rows, 1.0, 15.0)

            def allowed(size):
                return all(int(r["size_min"]) <= size <= int(r["size_max"])
                           and 1.0 <= gt.runtime(r, size) <= 15.0 for r in rows)
            self.assertTrue(allowed(s.lo) and allowed(s.hi), s)
            self.assertFalse(allowed(s.lo - 1) or allowed(s.hi + 1), s)

    def test_the_committed_lot_is_exactly_what_its_command_writes(self):
        # data/tasks_gen_s1.csv: the lot of the GCP procedure. The workloads are
        # named, so that the command gives the same lot once sort_task is measured.
        with tempfile.TemporaryDirectory() as folder:
            out = os.path.join(folder, "tasks.csv")
            with contextlib.redirect_stdout(io.StringIO()):
                code = gt.main(["--seed", "1", "--count", "18", "--out", out,
                                "--workloads", "cpu_task,memory_task,matrix_task"])
            self.assertEqual(code, 0)
            with open(out, "rb") as f:
                written = f.read()
        with open(os.path.join(ROOT, "data", "tasks_gen_s1.csv"), "rb") as f:
            committed = f.read().replace(b"\r\n", b"\n")
        self.assertEqual(written, committed,
                         "data/tasks_gen_s1.csv is not what its command writes any more "
                         "(did model.csv change?). If the lot was already run on GCP, keep "
                         "the file and change this test: do not regenerate it.")

    def test_runtime_is_the_one_the_go_scheduler_writes(self):
        # greedy_scheduler wrote predicted_runtime_s with its own copy of the
        # formula, for the sizes of the generated lot (none of them was profiled)
        machine_type = dict((m["vm"], m["machine_type"])
                            for m in gt.read_csv(self.machines_path))
        rows = gt.read_csv(os.path.join(ROOT, "data", "allocation_greedy_gen_s1.csv"))
        self.assertEqual(len(rows), 18)
        for r in rows:
            (row,) = [m for m in self.model[r["workload"]]
                      if m["machine_type"] == machine_type[r["vm"]]]
            self.assertEqual("%.2f" % gt.runtime(row, int(r["size"])),
                             r["predicted_runtime_s"], r["task_id"])


if __name__ == "__main__":
    unittest.main()

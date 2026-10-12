#!/usr/bin/env python3
"""Concurrency benchmark for docreader PDF parsing.

Compares the post-fix concurrency model (multiple gRPC workers + a process-wide
pdfium lock, ``DOCREADER_PDFIUM_SERIALIZE=true``) against the old
``DOCREADER_GRPC_MAX_WORKERS=1`` workaround, over a batch of PDFs.

It drives the **real** docreader ``PDFParser`` code paths -- the per-page
router, the ``pdfium_lock()`` critical sections and the process-parallel
scanned-page renderer -- not a reimplementation. Each configuration runs in its
own subprocess so process-global state (pdfium, CONFIG, process pools) never
leaks between runs, exactly like separate server instances.

Emulating gRPC workers
----------------------
The server serves parses from ``ThreadPoolExecutor(max_workers=grpc_max_workers)``
(main.py). So:

* ``--workers 1``  == ``DOCREADER_GRPC_MAX_WORKERS=1`` (old workaround)
* ``--workers N``  == ``DOCREADER_GRPC_MAX_WORKERS=N`` (new scheme)

The batch is submitted to a thread pool of that size; wall time is the
end-to-end makespan of the whole batch.

Usage
-----
    # full default matrix on all datasets
    python docreader/benchmarks/bench_concurrency.py

    # quick smoke test
    python docreader/benchmarks/bench_concurrency.py \
        --datasets text --configs w1 w4 --text-docs 3 --text-pages 3

    # focus on the scanned/render path
    python docreader/benchmarks/bench_concurrency.py \
        --datasets scanned --configs w1 w4 w4_render4
"""

from __future__ import annotations

import argparse
import json
import os
import random
import subprocess
import sys
import time
import traceback
from concurrent.futures import ThreadPoolExecutor, as_completed

# --------------------------------------------------------------------------
# Bootstrap: make ``docreader.*`` importable without executing the heavyweight
# ``docreader/parser/__init__.py`` (which pulls in optional deps like textract).
# Injecting the parent packages into sys.modules short-circuits their __init__
# while still letting real submodules (config / concurrency / pdf_parser) load.
# This runs on re-import in multiprocessing children too, which is required for
# the scanned-page process pool to import ``docreader.parser.pdf_parser``.
# --------------------------------------------------------------------------
import types

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.dirname(os.path.dirname(_HERE))
_DOCREADER_DIR = os.path.join(_REPO_ROOT, "docreader")

if _REPO_ROOT not in sys.path:
    sys.path.insert(0, _REPO_ROOT)

if "docreader" not in sys.modules:
    _pkg = types.ModuleType("docreader")
    _pkg.__path__ = [_DOCREADER_DIR]
    sys.modules["docreader"] = _pkg

if "docreader.parser" not in sys.modules:
    _sub = types.ModuleType("docreader.parser")
    _sub.__path__ = [os.path.join(_DOCREADER_DIR, "parser")]
    sys.modules["docreader.parser"] = _sub


# --------------------------------------------------------------------------
# Dataset generation (cached on disk)
# --------------------------------------------------------------------------

def _gen_text_pdf(path: str, pages: int, lines_per_page: int) -> None:
    """A native-text PDF: pdfium extracts the text layer (pass 1 dominates)."""
    from reportlab.lib.pagesizes import A4
    from reportlab.pdfgen import canvas

    w, h = A4
    c = canvas.Canvas(path, pagesize=A4)
    sentence = (
        "The quick brown fox jumps over the lazy dog 0123456789 "
        "alpha beta gamma delta epsilon zeta eta theta iota kappa."
    )
    for p in range(pages):
        c.setFont("Helvetica", 10)
        y = h - 60
        for i in range(lines_per_page):
            c.drawString(50, y, f"[p{p + 1}] line {i + 1}: {sentence}")
            y -= 12
            if y < 60:
                break
        c.showPage()
    c.save()


def _gen_scanned_pdf(path: str, pages: int, size: tuple[int, int]) -> None:
    """An image-only PDF: every page is one full-page image -> routed to the
    scanned path and rendered in the process pool."""
    from PIL import Image, ImageDraw

    random.seed(1234)
    imgs = []
    for p in range(pages):
        im = Image.new("RGB", size, (246, 246, 242))
        d = ImageDraw.Draw(im)
        for i in range(70):
            y = 40 + i * 24
            if y > size[1] - 40:
                break
            d.line([(40, y), (size[0] - 40, y)], fill=(190, 190, 190), width=1)
            d.text(
                (52, y - 9),
                f"SCANNED page {p + 1} row {i + 1}  lorem ipsum dolor sit amet",
                fill=(15, 15, 15),
            )
        for _ in range(2000):
            d.point(
                (random.randrange(size[0]), random.randrange(size[1])),
                fill=(random.randrange(60, 220),) * 3,
            )
        imgs.append(im)
    imgs[0].save(
        path, "PDF", save_all=True, append_images=imgs[1:], resolution=150
    )


def _dataset_spec(args, name: str) -> list[tuple[str, str]]:
    """Return [(filename, path)] for a dataset name."""
    d = args.data_dir
    os.makedirs(d, exist_ok=True)
    items: list[tuple[str, str]] = []

    def add(kind: str, n_docs: int, n_pages: int):
        for k in range(n_docs):
            fn = f"{kind}_{n_pages}p_{k}.pdf"
            path = os.path.join(d, fn)
            if not os.path.exists(path):
                if kind == "text":
                    _gen_text_pdf(path, n_pages, args.text_lines)
                else:
                    _gen_scanned_pdf(path, n_pages, (args.scan_w, args.scan_h))
            items.append((fn, path))

    if name == "text":
        add("text", args.text_docs, args.text_pages)
    elif name == "scanned":
        add("scan", args.scanned_docs, args.scanned_pages)
    elif name == "mixed":
        add("text", args.text_docs, args.text_pages)
        add("scan", args.scanned_docs, args.scanned_pages)
    else:
        raise SystemExit(f"unknown dataset: {name}")
    return items


# --------------------------------------------------------------------------
# Batch runner
# --------------------------------------------------------------------------

def _parse_one(path: str):
    from docreader.parser.pdf_parser import PDFParser

    with open(path, "rb") as f:
        content = f.read()
    parser = PDFParser(file_name=os.path.basename(path), file_type="pdf")
    doc = parser.parse(content)
    return len(doc.content or ""), len(getattr(doc, "images", None) or {})


def run_batch(paths: list[str], workers: int):
    """Run one parse per path; returns (wall_seconds, errors)."""
    errors: list[tuple[str, str]] = []
    t0 = time.perf_counter()
    if workers <= 1:
        for p in paths:
            try:
                _parse_one(p)
            except Exception as e:  # noqa: BLE001
                errors.append((os.path.basename(p), f"{type(e).__name__}: {e}"))
    else:
        with ThreadPoolExecutor(max_workers=workers) as ex:
            futs = {ex.submit(_parse_one, p): p for p in paths}
            for fut in as_completed(futs):
                p = futs[fut]
                try:
                    fut.result()
                except Exception as e:  # noqa: BLE001
                    errors.append((os.path.basename(p), f"{type(e).__name__}: {e}"))
    return time.perf_counter() - t0, errors


# --------------------------------------------------------------------------
# Config matrix
# --------------------------------------------------------------------------

_SER = {"DOCREADER_PDFIUM_SERIALIZE": "true"}

CONFIGS: dict[str, dict] = {
    # --- core comparison: old workaround vs new scheme ---
    "w1":           dict(workers=1, env=dict(_SER)),
    "w4":           dict(workers=4, env=dict(_SER)),
    "w8":           dict(workers=8, env=dict(_SER)),
    # --- unsafe baseline: no in-process serialisation -> original bug ---
    "w4_nolock":    dict(workers=4, env={"DOCREADER_PDFIUM_SERIALIZE": "false"}),
    "w8_nolock":    dict(workers=8, env={"DOCREADER_PDFIUM_SERIALIZE": "false"}),
    # --- render knobs: the real lever for scanned PDFs ---
    # control: 1 worker + render knob does nothing (only one request exists)
    "w1_render4":   dict(workers=1, env={**_SER, "DOCREADER_PDF_RENDER_MAX_WORKERS": "4"}),
    "w4_render4":   dict(workers=4, env={**_SER, "DOCREADER_PDF_RENDER_MAX_WORKERS": "4"}),
    "w8_render4":   dict(workers=8, env={**_SER, "DOCREADER_PDF_RENDER_MAX_WORKERS": "4"}),
    "w4_render8":   dict(workers=4, env={**_SER, "DOCREADER_PDF_RENDER_MAX_WORKERS": "8"}),
    # intra-document render parallelism off
    "w4_rpar1":     dict(workers=4, env={**_SER, "DOCREADER_PDF_RENDER_PARALLELISM": "1"}),
    # --- horizontal scaling (emulated replicas: N separate OS processes) ---
    # each "replica" is one docreader container: 1 gRPC worker, its own pdfium.
    "rep2":         dict(workers=1, replicas=2, env=dict(_SER)),
    "rep4":         dict(workers=1, replicas=4, env=dict(_SER)),
    "rep8":         dict(workers=1, replicas=8, env=dict(_SER)),
    # replicas with reduced intra-doc render parallelism (avoid oversubscription
    # when replicas x parallelism would exceed CPU count)
    "rep4_rpar2":   dict(workers=1, replicas=4,
                         env={**_SER, "DOCREADER_PDF_RENDER_PARALLELISM": "2"}),
    "rep8_rpar1":   dict(workers=1, replicas=8,
                         env={**_SER, "DOCREADER_PDF_RENDER_PARALLELISM": "1"}),
}

DEFAULT_CONFIGS = ["w1", "w4", "w8"]
DEFAULT_DATASETS = ["text", "scanned", "mixed"]


# --------------------------------------------------------------------------
# Worker mode: run ONE config and emit a JSON result line
# --------------------------------------------------------------------------

def worker_main(args) -> int:
    from docreader.config import CONFIG

    paths = [p for _, p in _dataset_spec(args, args.dataset)]

    # Shard slicing: emulate one replica taking a 1/N slice of the batch.
    if args.shard_count > 1:
        paths = paths[args.shard_index :: args.shard_count]

    if args.warmup:
        try:
            run_batch(paths[:1], 1)
        except Exception:  # noqa: BLE001
            pass

    times = []
    errors: list[tuple[str, str]] = []
    for _ in range(args.repeat):
        dur, errs = run_batch(paths, args.workers)
        times.append(dur)
        errors = errs

    result = {
        "dataset": args.dataset,
        "workers": args.workers,
        "docs": len(paths),
        "pdfium_serialize": CONFIG.pdfium_serialize,
        "pdf_render_parallelism": CONFIG.pdf_render_parallelism,
        "pdf_render_max_workers": CONFIG.pdf_render_max_workers,
        "best_s": min(times),
        "all_s": times,
        "n_errors": len(errors),
        "errors": errors[:5],
    }
    print("BENCH_RESULT " + json.dumps(result), flush=True)
    return 0


# --------------------------------------------------------------------------
# Driver mode: run the matrix, print a comparison table
# --------------------------------------------------------------------------

def _run_cmd(args, dataset: str, workers: int, cfg, shard_index: int, shard_count: int):
    cmd = [
        sys.executable, os.path.abspath(__file__), "run",
        "--dataset", dataset,
        "--workers", str(workers),
        "--data-dir", args.data_dir,
        "--repeat", str(args.repeat),
        "--text-docs", str(args.text_docs),
        "--text-pages", str(args.text_pages),
        "--text-lines", str(args.text_lines),
        "--scanned-docs", str(args.scanned_docs),
        "--scanned-pages", str(args.scanned_pages),
        "--scan-w", str(args.scan_w),
        "--scan-h", str(args.scan_h),
        "--shard-index", str(shard_index),
        "--shard-count", str(shard_count),
    ]
    if not args.warmup:
        cmd.append("--no-warmup")
    return cmd


def _failed(dataset: str, cfg, note: str) -> dict:
    return {
        "dataset": dataset,
        "workers": cfg["workers"],
        "best_s": float("nan"),
        "n_errors": -1,
        "errors": [["SUBPROCESS_FAILED", note]],
        "pdfium_serialize": None,
        "pdf_render_parallelism": None,
        "pdf_render_max_workers": None,
    }


def _run_one(dataset: str, cfg_name: str, args) -> dict:
    cfg = CONFIGS[cfg_name]
    env = os.environ.copy()
    env.update(cfg["env"])
    env["LOG_LEVEL"] = "ERROR"

    replicas = int(cfg.get("replicas", 1))

    # Horizontal scaling: launch `replicas` independent OS processes, each
    # handling a 1/N slice (== N docreader containers behind one client).
    # Group time is the slowest replica (the batch makespan).
    if replicas > 1:
        procs = [
            subprocess.Popen(
                _run_cmd(args, dataset, cfg["workers"], cfg, i, replicas),
                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
            for i in range(replicas)
        ]
        shard_results = []
        for p in procs:
            out, err = p.communicate()
            got = None
            for line in out.splitlines():
                if line.startswith("BENCH_RESULT "):
                    got = json.loads(line[len("BENCH_RESULT "):])
            shard_results.append(
                got or _failed(dataset, cfg, f"rc={p.returncode} {err.strip()[-200:]}")
            )
        ok = [r["best_s"] for r in shard_results if r["best_s"] == r["best_s"]]
        merged = dict(shard_results[0])
        merged["best_s"] = max(ok) if ok else float("nan")
        merged["replicas"] = replicas
        merged["n_errors"] = sum(r.get("n_errors", 0) for r in shard_results)
        merged["all_s"] = [r["best_s"] for r in shard_results]
        merged["errors"] = [e for r in shard_results for e in r.get("errors", [])][:5]
        return merged

    proc = subprocess.run(
        _run_cmd(args, dataset, cfg["workers"], cfg, 0, 1),
        env=env, capture_output=True, text=True,
    )
    for line in proc.stdout.splitlines():
        if line.startswith("BENCH_RESULT "):
            return json.loads(line[len("BENCH_RESULT "):])
    return _failed(
        dataset, cfg, f"rc={proc.returncode} stderr_tail={proc.stderr.strip()[-400:]}"
    )


def driver_main(args) -> int:
    print(f"repo root   : {_REPO_ROOT}")
    print(f"python      : {sys.executable}")
    print(f"cpu count   : {os.cpu_count()}")
    print(f"data dir    : {args.data_dir}")
    print(f"configs     : {', '.join(args.configs)}")
    print(f"datasets    : {', '.join(args.datasets)}")
    print(f"repeat      : {args.repeat} (reporting best-of)")
    print()

    results: dict[str, dict[str, dict]] = {}
    for dataset in args.datasets:
        results[dataset] = {}
        print(f"=== dataset: {dataset} ===")
        for cfg_name in args.configs:
            r = _run_one(dataset, cfg_name, args)
            results[dataset][cfg_name] = r
            err = f"  ERRORS={r['n_errors']}" if r.get("n_errors") else ""
            rep = f" replicas={r['replicas']}" if r.get("replicas") else ""
            print(
                f"  {cfg_name:<12} workers={r['workers']:<2}{rep} "
                f"best={r['best_s']:.3f}s{err}"
            )
        print()

    # comparison table vs w1 (if present)
    base = "w1" if "w1" in args.configs else args.configs[0]
    print("=" * 78)
    print(f"SPEEDUP vs '{base}'  (higher is better; >1 means faster)")
    print("=" * 78)
    header = f"{'dataset':<10} " + " ".join(
        f"{c:>12}" for c in args.configs
    )
    print(header)
    print("-" * len(header))
    for dataset in args.datasets:
        base_s = results[dataset].get(base, {}).get("best_s", float("nan"))
        cells = []
        for c in args.configs:
            s = results[dataset][c]["best_s"]
            if base_s and base_s == base_s and s == s and s > 0:
                cells.append(f"{base_s / s:>11.2f}x")
            else:
                cells.append(f"{'n/a':>12}")
        print(f"{dataset:<10} " + " ".join(cells))
    print()

    # raw seconds
    print(f"{'dataset':<10} " + " ".join(f"{c:>12}" for c in args.configs) + "   (seconds)")
    print("-" * (10 + 1 + 13 * len(args.configs)))
    for dataset in args.datasets:
        cells = []
        for c in args.configs:
            s = results[dataset][c]["best_s"]
            cells.append(f"{s:>11.3f}s" if s == s else f"{'FAIL':>12}")
        print(f"{dataset:<10} " + " ".join(cells))
    print()

    print("error counts:")
    for dataset in args.datasets:
        for c in args.configs:
            r = results[dataset][c]
            if r.get("n_errors"):
                print(f"  {dataset}/{c}: {r['n_errors']} error(s) -> {r['errors'][:2]}")
    return 0


# --------------------------------------------------------------------------

def build_parser() -> argparse.ArgumentParser:
    # Single parser with an optional mode positional, so both
    #   bench_concurrency.py [--datasets ... --configs ...]      (drive)
    #   bench_concurrency.py run --dataset X --workers N         (one config)
    # work, and bare invocation drives the default matrix.
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    p.add_argument("mode", nargs="?", choices=["drive", "run"], default="drive")
    p.add_argument("--dataset", help="dataset for 'run' mode")
    p.add_argument("--workers", type=int, help="worker count for 'run' mode")
    p.add_argument("--configs", nargs="+", default=DEFAULT_CONFIGS)
    p.add_argument("--datasets", nargs="+", default=DEFAULT_DATASETS)
    p.add_argument("--data-dir", default="/tmp/docreader_bench_data")
    p.add_argument("--text-docs", type=int, default=12)
    p.add_argument("--text-pages", type=int, default=8)
    p.add_argument("--text-lines", type=int, default=45)
    p.add_argument("--scanned-docs", type=int, default=6)
    p.add_argument("--scanned-pages", type=int, default=5)
    p.add_argument("--scan-w", type=int, default=1240)
    p.add_argument("--scan-h", type=int, default=1754)
    p.add_argument("--repeat", type=int, default=1)
    p.add_argument("--warmup", dest="warmup", action="store_true", default=True)
    p.add_argument("--no-warmup", dest="warmup", action="store_false")
    p.add_argument("--shard-index", type=int, default=0, help=argparse.SUPPRESS)
    p.add_argument("--shard-count", type=int, default=1, help=argparse.SUPPRESS)
    return p


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.mode == "run":
            if not args.dataset or args.workers is None:
                raise SystemExit("run mode needs --dataset and --workers")
            return worker_main(args)
        return driver_main(args)
    except KeyboardInterrupt:
        return 130
    except Exception:  # noqa: BLE001
        traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())

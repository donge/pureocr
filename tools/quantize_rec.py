#!/usr/bin/env python3
"""Quantize the CJK recognizer to int8 and drop the outputs the Go engine never
reads (blank / topv / topi are only used by Indra's Python n-gram decoder).

    # 1. dump calibration crops with the engine itself (same crops it sees)
    go run ./bench -dump-crops calib/ page1.png page2.png ...

    # 2. quantize (needs the fp32 rec_cjk.onnx, e.g. from
    #    huggingface-cli download datalab-to/indra-ocr rec_cjk.onnx)
    python3 tools/quantize_rec.py \
        --fp32 rec_cjk.fp32.onnx --models models/ --crops calib/ --out models-int8/

    # 3. run with the result
    go run ./example -models models-int8/ scan.png

Measured on real line crops (Apple M4, 4 threads): 1.93 -> 1.21 ms/line,
96.5% of the fp32 text unchanged, 10.2 -> 3.0 MB. Recalibrate on your own
domain: the activation ranges decide the quantized accuracy.
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
import time
from pathlib import Path

import numpy as np
import onnx
import onnxruntime as ort
from onnxruntime.quantization import (CalibrationDataReader, CalibrationMethod,
                                      QuantFormat, QuantType, quantize_static)

HEIGHT = 32


def load_crops(dir: Path) -> list[np.ndarray]:
    from PIL import Image

    out = []
    for p in sorted(dir.glob("*.png")):
        im = Image.open(p).convert("L")
        a = np.asarray(im, np.float32) / 255.0  # 255 = ink
        if a.shape[0] != HEIGHT:
            raise SystemExit(f"{p}: expected height {HEIGHT}, got {a.shape[0]}")
        out.append(a)
    if not out:
        raise SystemExit(f"no crops in {dir} (run `bench -dump-crops {dir} ...`)")
    return out


def batch(crops: list[np.ndarray]):
    w = max(int(np.ceil(c.shape[1] / 8) * 8) for c in crops)
    x = np.zeros((len(crops), 1, HEIGHT, w), np.float32)
    widths = np.zeros(len(crops), np.int64)
    for i, c in enumerate(crops):
        x[i, 0, :, : c.shape[1]] = c
        widths[i] = c.shape[1]
    return x, widths


class Reader(CalibrationDataReader):
    def __init__(self, crops: list[np.ndarray], limit: int = 250):
        self.items = crops[:limit]
        self.i = 0

    def get_next(self):
        if self.i >= len(self.items):
            return None
        x, w = batch([self.items[self.i]])
        self.i += 1
        return {"x": x, "widths": w}


def session(path: Path) -> ort.InferenceSession:
    so = ort.SessionOptions()
    so.intra_op_num_threads = 4
    so.inter_op_num_threads = 1
    so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    return ort.InferenceSession(str(path), so, providers=["CPUExecutionProvider"])


def run(sess, crop, vocab) -> str:
    x, w = batch([crop])
    ids, _, lengths = sess.run(["ids", "conf", "lengths"], {"x": x, "widths": w})
    out, prev = [], -1
    for i in range(int(lengths[0])):
        v = int(ids[0][i])
        if v == 0 or v == prev:
            prev = v
            continue
        prev = v
        if v <= len(vocab):
            out.append(vocab[v - 1])
    return "".join(out)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--fp32", required=True, help="fp32 rec_cjk.onnx")
    ap.add_argument("--models", default="models", help="model folder to copy the other files from")
    ap.add_argument("--crops", required=True, help="directory from `bench -dump-crops`")
    ap.add_argument("--out", default="models-int8", help="output folder")
    ap.add_argument("--limit", type=int, default=250, help="calibration crops to use")
    a = ap.parse_args()

    models, crops_dir, out = Path(a.models), Path(a.crops), Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    crops = load_crops(crops_dir)[: a.limit]
    print(f"calibration crops: {len(crops)}")

    # 1. keep only the outputs the Go engine reads
    pruned = out / "rec_cjk.pruned.onnx"
    onnx.utils.extract_model(str(a.fp32), str(pruned),
                             input_names=["x", "widths"],
                             output_names=["ids", "conf", "lengths"])

    # 2. int8 QDQ, per-channel weights, per-tensor uint8 activations
    quantize_static(str(pruned), str(out / "rec_cjk.onnx"), Reader(crops),
                    quant_format=QuantFormat.QDQ, per_channel=True,
                    activation_type=QuantType.QUInt8, weight_type=QuantType.QInt8,
                    calibrate_method=CalibrationMethod.MinMax,
                    op_types_to_quantize=["Conv", "MatMul"])
    pruned.unlink()

    # 3. the other models are copied unchanged
    for name in ("det.onnx", "rec.onnx", "script.onnx", "orient.onnx", "meta.json"):
        src = models / name
        if src.exists():
            shutil.copy2(src, out / name)

    vocab = json.loads(json.loads((models / "meta.json").read_text())["vocab_cjk"])["chars"]

    # 4. report speed and text agreement on the crops (fp32 vs int8)
    fp, q = session(Path(a.fp32)), session(out / "rec_cjk.onnx")
    held = crops[: min(60, len(crops))]

    def bench(s) -> float:
        for c in held[:10]:
            run(s, c, vocab)
        t = time.perf_counter()
        for c in held:
            run(s, c, vocab)
        return 1000 * (time.perf_counter() - t) / len(held)

    import difflib
    sims = [difflib.SequenceMatcher(None, run(fp, c, vocab), run(q, c, vocab)).ratio() for c in held]
    print(f"fp32 {bench(fp):.2f} ms/line | int8 {bench(q):.2f} ms/line | "
          f"text agreement {100 * np.mean(sims):.1f}%")
    size = (out / "rec_cjk.onnx").stat().st_size / 1e6
    print(f"wrote {out} (rec_cjk.onnx {size:.1f} MB)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

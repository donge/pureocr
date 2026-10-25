# Model weights

The ONNX models in this directory come from **Indra OCR** by Datalab
(https://github.com/datalab-to/indra, https://huggingface.co/datalab-to/indra-ocr),
and are redistributed here unmodified:

| file            | role                                  |
|-----------------|---------------------------------------|
| `det.onnx`      | text-line detector                    |
| `rec.onnx`      | Latin recognizer (3x tall window)     |
| `rec_cjk.onnx`  | Chinese / CJK recognizer              |
| `script.onnx`   | per-line script classifier            |
| `orient.onnx`   | page orientation (quarter turns)      |
| `meta.json`     | vocabularies and input geometry       |

`rec_cjk.onnx` here is **int8-quantized** and has its unused outputs removed
(`blank`/`topv`/`topi`); see `tools/quantize_rec.py` and the README to rebuild it
from the original fp32 weight file. The other models are unmodified.

They are licensed under the **AI PUBS OPEN RAIL-M LICENSE (MODIFIED)**; see
[`LICENSE`](LICENSE) in this directory. The licence is free for research,
personal use and companies under USD 20M in funding/revenue, with the use
restrictions stated there. For other terms see https://www.datalab.to/pricing.

The Go code in this repository is independent of the model licence.

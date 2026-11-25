"""Writes tiny.tflite, the keyword model runner's test model: our own, never the vendor's.

Run it in the pinned TensorFlow image (the converter that made the committed file), from the repo root:

    docker run --rm -v "$PWD/media/tests/kws:/kws" \
      tensorflow/tensorflow:2.20.0@sha256:6b0a7db409e62b7cf188ce72074c520af3e8c21f6d4d5b206f700520594dfb5a \
      python3 -I /kws/make_tiny_model.py /kws/tiny.tflite

It has the stock «Маруся» model's interface and op class, so the runner's whole path runs without the vendor model:

- signature serving_default: input args_0 float32 [1, 8, 40]; outputs pos_neg_probs [1, 8, 1, 2], kw_loc [1, 8, 1, 1]
  and int_unint_probs [1, 8, 1, 2], float32, which the subgraph lists in another order (kw_loc, int_unint_probs,
  pos_neg_probs, as the stock model's does), so a runner must map them by name;
- int8 inside, with a float32 QUANTIZE in and DEQUANTIZE out;
- a streaming state in a resource variable (VAR_HANDLE, READ_VARIABLE, ASSIGN_VARIABLE): the last 2 frames of the
  previous call, which the first 2 output frames see through a 3-frame convolution. A CALL_ONCE init subgraph sets it
  to 12.0, not to 0, so a reset that only clears the variables differs from one that runs the init subgraph again;
- only ops the runner's resolver registers, 16 of its 17 (all but PAD): CONCATENATION, STRIDED_SLICE, RESHAPE,
  CONV_2D, DEPTHWISE_CONV_2D, ADD, LOGISTIC, MUL, MAX_POOL_2D and SOFTMAX besides the above.

int_unint_probs leans to index 0 and pos_neg_probs to index 1 (fixed biases), so a runner that swaps them is caught;
kw_loc has no bias and follows the input. Weights and calibration data come from a fixed integer generator, not a
random library, and oneDNN is off, so a re-run with the same image writes the same bytes.
"""

import os
import sys
import tempfile

# before tensorflow loads: onednn's kernels may round differently from run to run
os.environ["TF_ENABLE_ONEDNN_OPTS"] = "0"

import numpy as np
import tensorflow as tf

FRAMES, MELS, KEEP, CHANNELS = 8, 40, 2, 8
STATE_INIT = 12.0


def lcg(n, lo, hi, seed):
    # numerical recipes' 32-bit lcg, as make_vectors.py: the same values everywhere
    out = np.empty(n, dtype=np.float64)
    x = seed & 0xFFFFFFFF
    for i in range(n):
        x = (1664525 * x + 1013904223) & 0xFFFFFFFF
        out[i] = lo + (hi - lo) * (x / 4294967296.0)
    return out


def weights(shape, scale, seed):
    return tf.constant(lcg(int(np.prod(shape)), -scale, scale, seed).reshape(shape).astype(np.float32))


class Tiny(tf.Module):
    def __init__(self):
        super().__init__()
        self.state = tf.Variable(np.full([1, KEEP, MELS], STATE_INIT, dtype=np.float32), trainable=False, name="state")
        self.conv = weights([KEEP + 1, 1, MELS, CHANNELS], 0.02, 1)
        self.depthwise = weights([3, 1, CHANNELS, 1], 0.5, 2)
        self.gate = weights([1, 1, CHANNELS, CHANNELS], 0.3, 3)
        self.pos_neg = weights([1, 1, CHANNELS, 2], 0.3, 4)
        self.pos_neg_bias = tf.constant([-1.5, 1.5], dtype=tf.float32)
        self.kw_loc = weights([1, 1, CHANNELS, 1], 0.3, 5)
        self.int_unint = weights([1, 1, CHANNELS, 2], 0.3, 6)
        self.int_unint_bias = tf.constant([1.5, -1.5], dtype=tf.float32)

    @tf.function(input_signature=[tf.TensorSpec([1, FRAMES, MELS], tf.float32, name="args_0")])
    def __call__(self, args_0):
        full = tf.concat([self.state.read_value(), args_0], axis=1)
        self.state.assign(full[:, -KEEP:, :])
        h = tf.reshape(full, [1, FRAMES + KEEP, 1, MELS])
        h = tf.nn.conv2d(h, self.conv, strides=1, padding="VALID")
        h = h + tf.nn.depthwise_conv2d(h, self.depthwise, strides=[1, 1, 1, 1], padding="SAME")
        h = h * tf.sigmoid(tf.nn.conv2d(h, self.gate, strides=1, padding="VALID"))
        h = tf.nn.max_pool2d(h, ksize=[2, 1], strides=1, padding="SAME")
        pos_neg = tf.nn.softmax(tf.nn.conv2d(h, self.pos_neg, strides=1, padding="VALID") + self.pos_neg_bias)
        kw_loc = tf.sigmoid(tf.nn.conv2d(h, self.kw_loc, strides=1, padding="VALID"))
        int_unint = tf.nn.softmax(tf.nn.conv2d(h, self.int_unint, strides=1, padding="VALID") + self.int_unint_bias)
        return {"pos_neg_probs": pos_neg, "kw_loc": kw_loc, "int_unint_probs": int_unint}


def calibration():
    # feature calls in the stock model's input range (about -78 to +64 dB), in sequence so the state is calibrated too
    for i in range(64):
        yield [lcg(FRAMES * MELS, -78.0, 64.0, 100 + i).reshape(1, FRAMES, MELS).astype(np.float32)]


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else "tiny.tflite"
    tf.config.experimental.enable_op_determinism()
    module = Tiny()
    with tempfile.TemporaryDirectory() as saved:
        tf.saved_model.save(module, saved, signatures={"serving_default": module.__call__.get_concrete_function()})
        converter = tf.lite.TFLiteConverter.from_saved_model(saved)
        converter.optimizations = [tf.lite.Optimize.DEFAULT]
        converter.representative_dataset = calibration
        converter.target_spec.supported_ops = [tf.lite.OpsSet.TFLITE_BUILTINS_INT8]
        converter.inference_input_type = tf.float32
        converter.inference_output_type = tf.float32
        converter.experimental_enable_resource_variables = True
        converter._experimental_variable_quantization = True
        model = converter.convert()
    with open(out, "wb") as f:
        f.write(model)
    print(f"{out}: {len(model)} bytes", file=sys.stderr)


if __name__ == "__main__":
    main()

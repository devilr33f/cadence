#pragma once

// ffmpeg's headers are c without extern "C" guards of their own
extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/dict.h>
#include <libavutil/error.h>
#include <libavutil/opt.h>
#include <libavutil/samplefmt.h>
}

// stb_image's implementation for the smoke test alone: it reads the screenshots the sim wrote without linking the
// screen's libraries (which carry their own implementation)
#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#pragma GCC diagnostic ignored "-Wmissing-field-initializers"
#endif
#define STB_IMAGE_IMPLEMENTATION
#define STBI_NO_STDIO
#include <stb/stb_image.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include "audio/stock-library.h"

#include <dlfcn.h>

namespace cadence {

namespace {

class DynamicLibrary final : public StockLibrary {
 public:
  explicit DynamicLibrary(void* handle) : handle_(handle) {}
  ~DynamicLibrary() override { dlclose(handle_); }

  DynamicLibrary(const DynamicLibrary&) = delete;
  DynamicLibrary& operator=(const DynamicLibrary&) = delete;

  void* symbol(const char* name) const override { return dlsym(handle_, name); }

 private:
  void* handle_;
};

}  // namespace

StockLibraryResult openStockLibrary(const std::filesystem::path& library) {
  // nodelete keeps the stock code mapped for any thread or exit handler it registered
  void* handle = dlopen(library.c_str(), RTLD_NOW | RTLD_LOCAL | RTLD_NODELETE);
  if (handle == nullptr)
    return StockError{StockErrorCode::LibraryLoadFailed, text::boundedMessage(dlerror())};
  return std::shared_ptr<StockLibrary>(std::make_shared<DynamicLibrary>(handle));
}

}  // namespace cadence

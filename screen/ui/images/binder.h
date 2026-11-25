#pragma once

#include "ui/images/image-cache.h"

#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ObserverPtr.h>

#include <cstdint>
#include <map>
#include <set>
#include <string>
#include <vector>

namespace cadence::screen::ui::images {

// puts the cache's images on the elements that ask for one: markup <div class="netimg" data-img="<id>"/> (rml's
// artwork helpers write it for an "img:<id>" art). once laid out, each asks the cache for its own box's size; when
// the image is ready its decorator becomes image("cimg:<key>#<gen>" cover), which the renderer loads from the cache.
// until then the slot shows its fill (no spinner per poster: section 9's skeleton is the row's). a shown image is
// pinned, so it is never evicted while on screen; an evicted one releases its texture. data-img-max caps the decoded
// width, data-img-blur softens it (images/blur.h); a bound element gets the class bound. ui thread only
class ImageBinder {
 public:
  // uploadBudget: the decoded bytes newly put on screen a pass (a poll and its scans), so a row of posters landing at
  // once uploads over a few frames instead of stalling one (at least one image a pass, whatever its size)
  explicit ImageBinder(ImageCache& cache, std::size_t uploadBudget = kUploadBudget);

  static constexpr std::size_t kUploadBudget = 2u << 20;
  ~ImageBinder();

  // finds unbound images in root and binds or requests them; true when something changed on screen
  bool scan(Rml::Element* root);
  // polls the cache (decodes that finished, evictions); true when a scan should run
  bool poll();
  // the renderer found no pixels for a source it was asked to load again (a lost context): those elements ask again
  void lost(const std::string& source);

  // "cimg:<key>#<gen>" -> key, or "" for anything else
  static std::string keyOfSource(const std::string& source);

  [[nodiscard]] std::size_t bound() const { return bound_.size(); }
  [[nodiscard]] std::size_t waiting() const { return waiting_; }
  // ready images the budget held back: the next pass binds them (the ui scans again at once)
  [[nodiscard]] std::size_t deferred() const { return deferred_; }

 private:
  struct Bound {
    Rml::ObserverPtr<Rml::Element> element;
    std::string key;
  };

  void release();

  ImageCache& cache_;
  std::vector<Bound> bound_;
  std::map<std::string, std::string> sources_;
  std::set<std::string> lostKeys_;
  std::uint64_t gen_ = 0;
  std::size_t waiting_ = 0;
  std::size_t uploadBudget_;
  std::size_t budgetLeft_;
  bool boundThisPass_ = false;
  std::size_t deferred_ = 0;
};

}  // namespace cadence::screen::ui::images
